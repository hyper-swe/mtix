// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// emitTestStore opens a fresh v2 store and a raw inspection handle.
// Returns (store, raw inspection DB, store cleanup, raw cleanup).
func emitTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "emit.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	return s, raw
}

// readOneEvent reads the last-inserted sync_events row by lamport_clock DESC.
func readOneEvent(t *testing.T, raw *sql.DB) *model.SyncEvent {
	t.Helper()
	row := raw.QueryRow(`
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash, sync_status, created_at
		FROM sync_events ORDER BY lamport_clock DESC LIMIT 1`)

	var (
		ev          model.SyncEvent
		opType      string
		syncStatus  string
		vcRaw       string
		payloadRaw  string
		createdAt   string
	)
	require.NoError(t, row.Scan(
		&ev.EventID, &ev.ProjectPrefix, &ev.NodeID, &opType, &payloadRaw,
		&ev.WallClockTS, &ev.LamportClock, &vcRaw,
		&ev.AuthorID, &ev.AuthorMachineHash, &syncStatus, &createdAt,
	))
	ev.OpType = model.OpType(opType)
	ev.SyncStatus = model.SyncStatus(syncStatus)
	ev.Payload = json.RawMessage(payloadRaw)
	require.NoError(t, json.Unmarshal([]byte(vcRaw), &ev.VectorClock))
	return &ev
}

func TestEmitEvent_HappyPath(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{"title":"x"}`),
		})
	}))

	ev := readOneEvent(t, raw)
	require.NotEmpty(t, ev.EventID, "event_id populated")
	require.Equal(t, "MTIX", ev.ProjectPrefix)
	require.Equal(t, "MTIX-1", ev.NodeID)
	require.Equal(t, model.OpCreateNode, ev.OpType)
	require.Equal(t, "alice", ev.AuthorID)
	require.Regexp(t, `^[a-f0-9]{16}$`, ev.AuthorMachineHash)
	require.Equal(t, model.SyncStatusPending, ev.SyncStatus)
	require.Equal(t, int64(1), ev.LamportClock, "first emit yields Lamport=1")
	require.Equal(t, int64(1), ev.VectorClock["alice"], "VC bumped for alice")
	require.JSONEq(t, `{"title":"x"}`, string(ev.Payload))
}

func TestEmitEvent_LamportMonotonicAcrossEmits(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			return emitEvent(ctx, tx, emitParams{
				NodeID:      "MTIX-" + strconv.Itoa(i+1),
				ProjectCode: "MTIX",
				OpType:      model.OpCreateNode,
				Author:      "alice",
				Payload:     json.RawMessage(`{}`),
			})
		}))
	}
	rows, err := raw.Query(`SELECT lamport_clock FROM sync_events ORDER BY lamport_clock`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	got := []int64{}
	for rows.Next() {
		var l int64
		require.NoError(t, rows.Scan(&l))
		got = append(got, l)
	}
	require.Equal(t, []int64{1, 2, 3, 4, 5}, got, "Lamport monotonic, no gaps")
}

func TestEmitEvent_VectorClockBumpsPerAuthor(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()
	for _, author := range []string{"alice", "alice", "bob", "alice", "bob"} {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			return emitEvent(ctx, tx, emitParams{
				NodeID:      "MTIX-1",
				ProjectCode: "MTIX",
				OpType:      model.OpUpdateField,
				Author:      author,
				Payload:     json.RawMessage(`{"field_name":"title","new_value":"\"x\""}`),
			})
		}))
	}
	ev := readOneEvent(t, raw)
	require.Equal(t, int64(3), ev.VectorClock["alice"])
	require.Equal(t, int64(2), ev.VectorClock["bob"])
}

func TestEmitEvent_MachineHashCachedAfterFirstEmit(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{}`),
		})
	}))
	ev1 := readOneEvent(t, raw)

	var stored string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.machine_hash'`,
	).Scan(&stored))
	require.Equal(t, ev1.AuthorMachineHash, stored,
		"first emit caches the computed hash into meta")

	// Second emit must read from meta — no recompute. We assert the
	// returned hash equals the stored value (recomputation would yield
	// the same value too, so this is necessary but not sufficient
	// without a stronger oracle).
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpUpdateField,
			Author:      "alice",
			Payload:     json.RawMessage(`{"field_name":"title","new_value":"\"y\""}`),
		})
	}))
	ev2 := readOneEvent(t, raw)
	require.Equal(t, ev1.AuthorMachineHash, ev2.AuthorMachineHash)
}

func TestEmitEvent_AtomicWithMutationOnError(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	// Trigger a tx that does an INSERT to sync_events and then returns
	// an error, forcing rollback. Asserts the sync_events row did NOT
	// persist — this is the in-process equivalent of the SIGKILL chaos
	// test in TestEmit_AtomicityUnderKill (which exercises kernel-level
	// rollback). Here we exercise the application-level rollback path
	// via WithTx error propagation.
	wantErr := errSentinelForTest
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{}`),
		}); err != nil {
			return err
		}
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&n))
	require.Equal(t, 0, n, "tx rollback must drop the would-have-been-emitted row")

	// Lamport must also be rolled back.
	var lamportRaw string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`,
	).Scan(&lamportRaw))
	require.Equal(t, "0", lamportRaw, "Lamport bump rolled back atomically")
}

func TestEmitEvent_AuthorSanitization(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alice", "alice"},
		{"agent-1", "agent-1"},
		{"claude-opus-4-7", "claude-opus-4-7"},
		{"Vimal Menon", "vimal-menon"},
		{"vimal.menon", "vimal-menon"},
		{"", "cli"},
		{"!@#$%", "cli"},
		{"UPPERCASE", "uppercase"}, // normalize-then-validate succeeds
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, sanitizeAuthorID(tc.in))
		})
	}
}

func TestProjectPrefixFromNodeID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"MTIX-1", "MTIX"},
		{"MTIX-1.2.3", "MTIX"},
		{"PROJ123-1", "PROJ123"},
		{"DEP_ADD-1", "DEP_ADD"},
		{"-1", ""},
		{"", ""},
		{"no-dash-but-lower", ""}, // lowercase prefix invalid
		{"BADPREFIX!-1", ""},      // non-alphanum char before dash
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, projectPrefixFromNodeID(tc.in))
		})
	}
}

func TestSanitizeAuthorID_LongAuthorTruncatesViaFallback(t *testing.T) {
	long := ""
	for i := 0; i < 65; i++ {
		long += "a"
	}
	require.Equal(t, "cli", sanitizeAuthorID(long),
		"longer-than-64 must hit the fallback (we don't truncate silently)")
}

func TestEmitEvent_ConcurrentEmittersAreSerialized(t *testing.T) {
	// SQLite WAL + WithTx serializes writers. Concurrent emit calls
	// should produce strictly monotonic Lamport values — never a tie or
	// a gap. This is the application-level guarantee the FR-18.18
	// singleton pusher chaos test relies on.
	s, raw := emitTestStore(t)
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			err := s.WithTx(ctx, func(tx *sql.Tx) error {
				return emitEvent(ctx, tx, emitParams{
					NodeID:      "MTIX-" + strconv.Itoa(i+1),
					ProjectCode: "MTIX",
					OpType:      model.OpCreateNode,
					Author:      "alice",
					Payload:     json.RawMessage(`{}`),
				})
			})
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	rows, err := raw.Query(`SELECT lamport_clock FROM sync_events ORDER BY lamport_clock`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var seen []int64
	for rows.Next() {
		var l int64
		require.NoError(t, rows.Scan(&l))
		seen = append(seen, l)
	}
	require.Len(t, seen, n)
	for i, l := range seen {
		require.Equal(t, int64(i+1), l, "Lamport %d must be position %d (no gaps, no ties)", l, i)
	}
}

// errSentinelForTest is a private sentinel for rollback testing.
var errSentinelForTest = &testRollbackError{}

type testRollbackError struct{}

func (e *testRollbackError) Error() string { return "test sentinel for rollback" }

func TestEmitEvent_CorruptedLamportMetaValueSurfacesError(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	_, err := raw.Exec(`UPDATE meta SET value = 'not-an-int' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{}`),
		})
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse lamport")
}

func TestEmitEvent_CorruptedVectorClockMetaValueSurfacesError(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	_, err := raw.Exec(`UPDATE meta SET value = '<<<not-json' WHERE key = 'meta.sync.vector_clock'`)
	require.NoError(t, err)

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{}`),
		})
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse vector_clock")
}

func TestEmitEvent_FallsBackToProjectCodeWhenNodeIDPrefixInvalid(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	// Node IDs without a valid prefix (no dash, lowercase, etc) cause
	// projectPrefixFromNodeID to return "" — emitEvent then uses
	// ProjectCode from the params.
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "no-dash-prefix",
			ProjectCode: "MTIX",
			OpType:      model.OpComment,
			Author:      "alice",
			Payload:     json.RawMessage(`{}`),
		})
	}))

	var got string
	require.NoError(t, raw.QueryRow(
		`SELECT project_prefix FROM sync_events WHERE node_id = 'no-dash-prefix'`,
	).Scan(&got))
	require.Equal(t, "MTIX", got)
}
