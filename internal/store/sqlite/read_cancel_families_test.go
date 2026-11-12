// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// readCancelIterations is how many cancel offsets each read family is tried
// with. A cancel only leaves a stale snapshot when it lands in the few
// microseconds between the driver stepping the first row and returning, so the
// test sweeps many offsets rather than relying on one.
const readCancelIterations = 1500

// readCancelRouteIterations is the sweep for the routes callers outside the
// store use (Query, QueryRow, ReadDB), the prepared-statement routes and the
// ack-info read. The window is a few microseconds wide, so a sweep that is
// merely long is flaky; with the shield removed these routes first fail
// between iteration 0 and about 5900 across runs (one 12000 sweep missed a route), so 20000 fails every run
// (red-on-base-2.txt).
const readCancelRouteIterations = 20000

// readCancelSlowIterations bounds the families whose single read is slow (the
// full integrity check), so the test stays fast under the race detector.
const readCancelSlowIterations = 300

// preparer is the prepare surface of a reader *sql.Conn and *sql.Tx.
type preparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// seedCancelFixture stores a small node tree so multi-row reads step a first
// row before the cancel can land.
func seedCancelFixture(t *testing.T, st *Store) {
	t.Helper()
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for i := 1; i <= 30; i++ {
		n := &model.Node{
			ID: fmt.Sprintf("MTIX-%d", i), Project: "MTIX", Seq: i, Title: "cancel fixture node",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
			NodeType: model.NodeTypeForDepth(0), ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, st.CreateNode(t.Context(), n))
	}
	// A fresh last-read row makes the inbox stamp reads step a row, not an empty set.
	_, err := st.writeDB.ExecContext(t.Context(),
		`INSERT INTO agent_inbox_reads(agent_id,last_read_at) VALUES('w',?)`, deliveryTime(time.Now()))
	require.NoError(t, err)
	_, err = st.writeDB.ExecContext(t.Context(),
		`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES('w',1,?)`, deliveryTime(time.Now()))
	require.NoError(t, err)
	for i := 1; i <= 3; i++ {
		c := &model.Node{
			ID: fmt.Sprintf("MTIX-1.%d", i), ParentID: "MTIX-1", Project: "MTIX", Seq: i, Depth: 1,
			Title: "cancel fixture child", Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1, NodeType: model.NodeTypeForDepth(1), ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, st.CreateNode(t.Context(), c))
	}
}

// cancelReadFamilies lists one representative read per family of reader-pool
// access: node reads, search, tree, stats, a read transaction, long
// integrity reads, inbox reads, and the exported Query, QueryRow and ReadDB
// handles that hand the pool to callers outside this package (MTIX-250).
func cancelReadFamilies(st *Store) map[string]func(context.Context) {
	drain := func(c context.Context, q string) {
		rows, err := st.Query(c, q)
		if err != nil {
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			_ = rows.Err()
		}
	}
	prepared := func(c context.Context, q preparer) {
		stmt, err := q.PrepareContext(c, `SELECT id, title FROM nodes ORDER BY id`)
		if err != nil {
			return
		}
		defer func() { _ = stmt.Close() }()
		rows, err := stmt.QueryContext(c)
		if err != nil {
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			_ = rows.Err()
		}
	}
	return map[string]func(context.Context){
		"Conn.PrepareContext": func(c context.Context) {
			conn, err := st.readDB.Conn(context.WithoutCancel(c))
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			prepared(c, conn)
		},
		"ReadDB.Conn.PrepareContext": func(c context.Context) {
			conn, err := st.ReadDB().Conn(context.WithoutCancel(c))
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			prepared(c, conn)
		},
		"Tx.PrepareContext": func(c context.Context) {
			tx, err := st.readDB.BeginTx(c, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return
			}
			defer func() { _ = tx.Rollback() }()
			prepared(c, tx)
		},
		"GetNode":           func(c context.Context) { _, _ = st.GetNode(c, "MTIX-3") },
		"GetDirectChildren": func(c context.Context) { _, _ = st.GetDirectChildren(c, "MTIX-1") },
		"ListNodes":         func(c context.Context) { _, _, _ = st.ListNodes(c, store.NodeFilter{}, store.ListOptions{Limit: 50}) },
		"SearchNodes": func(c context.Context) {
			_, _, _ = st.SearchNodes(c, "cancel", store.NodeFilter{}, store.ListOptions{Limit: 50})
		},
		"GetTree":            func(c context.Context) { _, _ = st.GetTree(c, "MTIX-1", 3) },
		"GetStats":           func(c context.Context) { _, _ = st.GetStats(c, "") },
		"readExportSnapshot": func(c context.Context) { _, _ = st.readExportSnapshot(c) },
		"Verify":             func(c context.Context) { _, _ = st.Verify(c) },
		"InboxAckInfo":       func(c context.Context) { _, _ = st.InboxAckInfo(c, "w", 1) },
		"InboxLastRead":      func(c context.Context) { _, _ = st.InboxLastRead(c, "w") },
		"readStampDue":       func(c context.Context) { _ = st.readStampDue(c, "w", time.Now()) },
		"InboxCount":         func(c context.Context) { _, _, _ = st.InboxCount(c, "w") },
		"Store.Query":        func(c context.Context) { drain(c, `SELECT id, title FROM nodes ORDER BY id`) },
		"Store.QueryRow": func(c context.Context) {
			var n int
			_ = st.QueryRow(c, `SELECT COUNT(*) FROM nodes`).Scan(&n)
		},
		"ReadDB.QueryContext": func(c context.Context) {
			rows, err := st.ReadDB().QueryContext(c, `SELECT id FROM nodes ORDER BY id`)
			if err != nil {
				return
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				_ = rows.Err()
			}
		},
	}
}

// TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot cancels a read of
// every family at a varying instant, on a reader pool of ONE connection so the
// next read reuses it, and asserts the next read sees a fresh write. A stale
// WAL snapshot left by a cancelled read would hide it (MTIX-250; the same
// property TestReadPaths_CancelledMidQuery_LeaveNoStaleSnapshot holds for the
// inbox reads of MTIX-112.3). Each family gets its own Store, because a stale
// connection never recovers and would fail every later family for it.
func TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot(t *testing.T) {
	for name := range cancelReadFamilies(nil) {
		t.Run(name, func(t *testing.T) {
			st, err := New(t.TempDir(), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })
			seedCancelFixture(t, st)
			st.readDB.SetMaxOpenConns(1)
			read := cancelReadFamilies(st)[name]
			iterations := readCancelIterations
			if name == "Verify" {
				iterations = readCancelSlowIterations
			}
			if sweepsRoute(name) {
				iterations = readCancelRouteIterations
			}
			for i := 0; i < iterations; i++ {
				ctx, cancel := context.WithCancel(t.Context())
				go func() { time.Sleep(time.Duration(i%40) * 5 * time.Microsecond); cancel() }()
				read(ctx)
				cancel()
				key := fmt.Sprintf("%s-%d", name, i)
				_, err := st.writeDB.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?)`, key, "v")
				require.NoError(t, err)
				rctx, rc := context.WithTimeout(t.Context(), 2*time.Second)
				var n int
				err = st.readDB.QueryRowContext(rctx, `SELECT COUNT(*) FROM meta WHERE key=?`, key).Scan(&n)
				rc()
				require.NoError(t, err, "iter %d", i)
				require.Equal(t, 1, n, "stale snapshot after cancelled %s, iter %d", name, i)
			}
		})
	}
}

// sweepsRoute reports whether the family is a route that needs the long sweep.
func sweepsRoute(name string) bool {
	switch name {
	case "Store.Query", "Store.QueryRow", "ReadDB.QueryContext", "InboxAckInfo",
		"Conn.PrepareContext", "ReadDB.Conn.PrepareContext", "Tx.PrepareContext":
		return true
	}
	return false
}
