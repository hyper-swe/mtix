// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of the held-push-event data access (MTIX-95.12): push holds an own
// event the hub would refuse in sync_quarantine with source push; the
// pull-side reads and writes of the table leave those rows alone.

// oldQuarantineDDL is sync_quarantine as MTIX-95.11 created it, before push
// holds: its CHECK allows only pull and sweep.
const oldQuarantineDDL = `CREATE TABLE sync_quarantine (
    event_id     TEXT PRIMARY KEY,
    source       TEXT NOT NULL CHECK (source IN ('pull', 'sweep')),
    raw_event    TEXT NOT NULL,
    reason       TEXT NOT NULL,
    first_seen   TEXT NOT NULL,
    last_attempt TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 1,
    cli_version  TEXT NOT NULL DEFAULT ''
)`

// pushHold is a held push event row as push records it.
func pushHold(id string) sqlite.QuarantinedEvent {
	return sqlite.QuarantinedEvent{EventID: id, RawEvent: `{"lamport_clock":2}`, Reason: "payload too large"}
}

// TestSchema_QuarantineTable_AcceptsPushSource: a fresh store, and a store
// whose sync_quarantine was created before push holds, both accept source
// push after opening; the older table's rows are kept, any other source is
// still refused, and opening again changes nothing.
func TestSchema_QuarantineTable_AcceptsPushSource(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("table from before push holds: %v", old), func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "q.db")
			s, err := sqlite.New(dbPath, slog.Default())
			require.NoError(t, err)
			if old {
				quarantineTx(t, s, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `DROP TABLE sync_quarantine`); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, oldQuarantineDDL); err != nil {
						return err
					}
					return sqlite.QuarantineEvent(ctx, tx, quarantineRow("pulled-1", `{"lamport_clock":1}`))
				})
			}
			require.NoError(t, s.Close())

			for open := 0; open < 2; open++ {
				s, db := schemaTestEnv(t, dbPath)
				require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold(fmt.Sprintf("held-%d", open))}, "v"))
				held, err := s.CountHeldPushEvents(ctx)
				require.NoError(t, err)
				require.Equal(t, open+1, held)
				pulled, err := s.CountQuarantined(ctx)
				require.NoError(t, err)
				want := 0
				if old {
					want = 1
				}
				require.Equal(t, want, pulled, "rows from before the widening are kept")
				_, err = db.Exec(`INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen, last_attempt)
					VALUES ('x', 'elsewhere', '{}', 'r', 't', 't')`)
				require.Error(t, err, "source is still pull, sweep or push")
				require.NoError(t, s.Close())
			}
		})
	}
}

// TestHoldPushEvents_RecordsSourcePushWithStoreClock: holds are stored with
// source push, the store's clock and the CLI version; holding an event again
// counts one more attempt and keeps the first reason; nothing to hold is a
// no-op.
func TestHoldPushEvents_RecordsSourcePushWithStoreClock(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	fixed := time.Date(2026, 9, 25, 8, 30, 0, 0, time.FixedZone("x", 3600))
	s.SetClock(func() time.Time { return fixed })
	require.Equal(t, fixed.UTC(), s.Now())

	require.NoError(t, s.HoldPushEvents(ctx, nil, "v1"))
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("e1"), pushHold("e2")}, "v1"))
	again := pushHold("e1")
	again.Reason = "another reason"
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{again}, "v2"))

	page, err := s.QuarantinePage(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, page, 2)
	for _, q := range page {
		require.Equal(t, sqlite.QuarantineSourcePush, q.Source)
		require.Equal(t, fixed.UTC().Format(time.RFC3339Nano), q.FirstSeen)
		require.Equal(t, "v1", q.CLIVersion)
		require.Equal(t, "payload too large", q.Reason)
	}
	attempts := map[string]int{page[0].EventID: page[0].Attempts, page[1].EventID: page[1].Attempts}
	require.Equal(t, map[string]int{"e1": 2, "e2": 1}, attempts)
	n, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

// TestPullQuarantineAccess_HeldPushRows_LeftAlone: the pull-side count,
// removal and clone reset act on pulled rows only; a held push event's row
// survives them all.
func TestPullQuarantineAccess_HeldPushRows_LeftAlone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	quarantineTx(t, s, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, quarantineRow("pulled", `{"lamport_clock":1}`))
	})
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("held")}, "v"))

	pulled, err := s.CountQuarantined(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, pulled, "CountQuarantined counts pulled events only")

	quarantineTx(t, s, func(tx *sql.Tx) error { return sqlite.RemoveQuarantined(ctx, tx, "held") })
	held, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, held, "RemoveQuarantined never removes a held push event")

	require.NoError(t, s.ClearQuarantine(ctx))
	pulled, err = s.CountQuarantined(ctx)
	require.NoError(t, err)
	require.Zero(t, pulled, "ClearQuarantine removes the pulled rows")
	held, err = s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, held, "ClearQuarantine keeps held push events")
}

// TestHeldPushEvents_QueueOrderLimitAndNode: held events are listed in the
// order push would send them (Lamport clock, then id), with node, op,
// payload and uid from their sync_events rows and the current number of the
// task each is about, at most limit of them; a hold whose event row is
// missing is still listed, with no node or op. A held creation renumbered
// on this machine and then soft-deleted still has its task found, under its
// new number.
func TestHeldPushEvents_QueueOrderLimitAndNode(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	var ids []string
	for i := 1; i <= 3; i++ {
		node := makeRootNode(fmt.Sprintf("PROJ-%d", i), "PROJ", fmt.Sprintf("node %d", i), now)
		node.Seq = i
		require.NoError(t, s.CreateNode(ctx, node))
		var id string
		require.NoError(t, s.QueryRow(ctx,
			`SELECT event_id FROM sync_events WHERE node_id = ?`, node.ID).Scan(&id))
		ids = append(ids, id)
	}
	var holds []sqlite.QuarantinedEvent
	for i := len(ids) - 1; i >= 0; i-- {
		holds = append(holds, pushHold(ids[i]))
	}
	holds = append(holds, pushHold("orphan"))
	require.NoError(t, s.HoldPushEvents(ctx, holds, "v"))
	require.NoError(t, s.RenumberSubtree(ctx, "PROJ-3", 9))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-9", false, "tester"))
	current := map[string]string{ids[0]: "PROJ-1", ids[1]: "PROJ-2", ids[2]: "PROJ-9"}

	all, err := s.HeldPushEvents(ctx, 10)
	require.NoError(t, err)
	var lamport int64
	for i := range all {
		require.True(t, all[i].EventID == "orphan" || all[i].Lamport > lamport, "Lamport clocks grow in queue order")
		lamport = all[i].Lamport
		if all[i].EventID != "orphan" {
			require.Contains(t, all[i].Payload, `"title"`, "the payload comes from the event row")
			require.Equal(t, all[i].EventID, all[i].UID, "a new node's creation carries its own id as uid")
			require.Equal(t, current[all[i].EventID], all[i].CurrentNodeID,
				"the task is found by the event's uid, soft-deleted or not")
		}
		all[i].Payload, all[i].Lamport, all[i].PushSubject = "", 0, sqlite.PushSubject{}
	}
	require.Equal(t, []sqlite.HeldPushEvent{
		{EventID: "orphan", Reason: "payload too large"},
		{EventID: ids[0], NodeID: "PROJ-1", OpType: "create_node", Reason: "payload too large"},
		{EventID: ids[1], NodeID: "PROJ-2", OpType: "create_node", Reason: "payload too large"},
		{EventID: ids[2], NodeID: "PROJ-3", OpType: "create_node", Reason: "payload too large"},
	}, all)
	two, err := s.HeldPushEvents(ctx, 2)
	require.NoError(t, err)
	for i := range two {
		two[i].Payload, two[i].Lamport, two[i].PushSubject = "", 0, sqlite.PushSubject{}
	}
	require.Equal(t, all[:2], two)
}

// TestHeldPushEvents_EditOfRenumberedTask_FoundByUID: a held edit (not a
// creation) is listed with its task found by the edit's uid, under the
// task's current number after a local renumber.
func TestHeldPushEvents_EditOfRenumberedTask_FoundByUID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "task", now)))
	title := "edited"
	require.NoError(t, s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Title: &title}))
	var create, edit string
	require.NoError(t, s.QueryRow(ctx,
		`SELECT event_id FROM sync_events WHERE node_id = 'PROJ-1' AND op_type = 'create_node'`).Scan(&create))
	require.NoError(t, s.QueryRow(ctx,
		`SELECT event_id FROM sync_events WHERE node_id = 'PROJ-1' AND op_type = 'update_field'`).Scan(&edit))
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold(edit)}, "v"))
	require.NoError(t, s.RenumberSubtree(ctx, "PROJ-1", 4))

	held, err := s.HeldPushEvents(ctx, -1)
	require.NoError(t, err)
	require.Len(t, held, 1)
	require.Equal(t, sqlite.PushSubject{UID: create, CurrentNodeID: "PROJ-4"}, held[0].PushSubject)
	require.Equal(t, "PROJ-1", held[0].NodeID, "the number the edit names stays as it was")
}

// TestSetPushHoldReasons_Relabel_KeepsAttemptsAndFirstSeen: a relabel of a
// kept dependent replaces its reason only; attempts, first_seen and
// last_attempt stay (MTIX-95.12).
func TestSetPushHoldReasons_Relabel_KeepsAttemptsAndFirstSeen(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	first := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return first })
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("h")}, "v"))
	require.NoError(t, s.SetPushHoldReasons(ctx, nil))

	s.SetClock(func() time.Time { return first.Add(time.Hour) })
	require.NoError(t, s.SetPushHoldReasons(ctx, map[string]string{"h": "depends on held create of x (P-9)"}))
	page, err := s.QuarantinePage(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "depends on held create of x (P-9)", page[0].Reason)
	require.Equal(t, 1, page[0].Attempts)
	require.Equal(t, first.Format(time.RFC3339Nano), page[0].FirstSeen)
	require.Equal(t, first.Format(time.RFC3339Nano), page[0].LastAttempt)
}

// TestNotePushHoldAttempts_ByKind_CountsOnlyThoseHolds: one more attempt,
// at the store's clock, is counted for every push hold whose reason starts
// with one of the kinds; other push holds and quarantined pulled events are
// left as they were, and no kinds counts nothing (MTIX-95.12).
func TestNotePushHoldAttempts_ByKind_CountsOnlyThoseHolds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	first := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return first })
	reasons := map[string]string{
		"clock": "temporary: clock: ahead", "dep": "depends on held create of x (P-1)",
		"size": "too large: payload", "refused": "refused: depth",
	}
	var holds []sqlite.QuarantinedEvent
	for id, reason := range reasons {
		h := pushHold(id)
		h.Reason = reason
		holds = append(holds, h)
	}
	require.NoError(t, s.HoldPushEvents(ctx, holds, "v"))
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
			EventID: "pulled", Source: "pull", RawEvent: "{}", Reason: "depends on held create of y (P-2)",
			FirstSeen: "t", LastAttempt: "t",
		})
	}))
	require.NoError(t, s.NotePushHoldAttempts(ctx, nil))

	s.SetClock(func() time.Time { return first.Add(time.Hour) })
	require.NoError(t, s.NotePushHoldAttempts(ctx, []string{"temporary: clock: ", "depends on held create of "}))
	page, err := s.QuarantinePage(ctx, nil, 10)
	require.NoError(t, err)
	got := map[string]int{}
	for _, q := range page {
		got[q.EventID] = q.Attempts
		if q.EventID == "clock" || q.EventID == "dep" {
			require.Equal(t, first.Add(time.Hour).Format(time.RFC3339Nano), q.LastAttempt)
		}
	}
	require.Equal(t, map[string]int{"clock": 2, "dep": 2, "size": 1, "refused": 1, "pulled": 1}, got)
}

// openLogged opens the store at dbPath with its log written to logs, and
// returns the stored CREATE statement of sync_quarantine; the store is
// closed again.
func openLogged(t *testing.T, dbPath string, logs *bytes.Buffer) string {
	t.Helper()
	s, err := sqlite.New(dbPath, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err)
	var ddl string
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sync_quarantine'`).Scan(&ddl))
	require.NoError(t, s.Close())
	return ddl
}

// TestSchema_QuarantineTable_RebuiltAtMostOnce: the widening rebuild runs
// on the first open of a table from before push holds only; a later open
// leaves the table's DDL unchanged and logs no rebuild, and a fresh store is
// never rebuilt.
func TestSchema_QuarantineTable_RebuiltAtMostOnce(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("table from before push holds: %v", old), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "q.db")
			s, err := sqlite.New(dbPath, slog.Default())
			require.NoError(t, err)
			if old {
				quarantineTx(t, s, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(context.Background(), `DROP TABLE sync_quarantine`); err != nil {
						return err
					}
					_, err := tx.ExecContext(context.Background(), oldQuarantineDDL)
					return err
				})
			}
			require.NoError(t, s.Close())

			var first, second bytes.Buffer
			ddl1 := openLogged(t, dbPath, &first)
			ddl2 := openLogged(t, dbPath, &second)
			require.Equal(t, old, bytes.Contains(first.Bytes(), []byte("sync_quarantine_widened")),
				"the first open rebuilds only a table from before push holds")
			require.Contains(t, ddl1, "'push'")
			require.Equal(t, ddl1, ddl2, "a second open leaves the table unchanged")
			require.NotContains(t, second.String(), "sync_quarantine_widened", "a second open rebuilds nothing")
		})
	}
}
