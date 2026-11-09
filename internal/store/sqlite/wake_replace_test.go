// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type wakeReplaceState struct {
	store         *sqlite.Store
	raw           *sql.DB
	data          *sqlite.ExportData
	kept, dropped int64
	keptMark      int64
	cursor        bool
	keptCursor    int64
	droppedCursor int64
}

func seedReplaceWakeState(t *testing.T, s *sqlite.Store, host string, cursor *int64) {
	t.Helper()
	key := model.WakeKey{AgentID: "worker", HostID: host, UID: "1000", Epoch: 7}
	regKey := sqlite.RegistrationKey{AgentID: key.AgentID, HostID: host, UID: key.UID}
	_, err := s.RegisterSessionAtomic(t.Context(), regKey, func(_ *sqlite.Registration, tail int64) (*sqlite.Registration, error) {
		key.EventSeq = tail
		return &sqlite.Registration{AgentID: key.AgentID, HostID: host, UID: key.UID, Harness: "pi", SessionHandle: "test-session",
			Origin: "manual", Epoch: key.Epoch, HighWater: tail, PendingHold: true,
			RegisteredAt: "2026-11-06T00:00:00Z", LastSeen: "2026-11-06T00:00:00Z", RegistrarVersion: "test", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	_, err = s.AcquireWakeLease(t.Context(), sqlite.WakeLeaseKey{AgentID: key.AgentID, HostID: host, UID: key.UID}, "", time.Minute)
	require.NoError(t, err)
	// A real contender marks the retained lease dirty without changing ownership.
	_, err = s.AcquireWakeLease(t.Context(), sqlite.WakeLeaseKey{AgentID: key.AgentID, HostID: host, UID: key.UID}, "", time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	if cursor != nil {
		require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), host, key.UID, *cursor, time.Minute))
	}
	result, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, func(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{BoardID: p.BoardCandidate, Allow: true}, nil
	})
	require.NoError(t, err)
	require.True(t, result.Won)
	require.NoError(t, s.RecordWakeResult(t.Context(), result.Attempt, model.WakeResult{Outcome: model.WakeSent, Route: "socket-push"}))
	key.Epoch++
	_, won, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
}

func newWakeReplaceState(t *testing.T, floor string, empty bool) wakeReplaceState {
	t.Helper()
	s, raw := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	kept, err := s.JournalTail(t.Context())
	require.NoError(t, err)
	keptCursor := kept
	if floor == "below" {
		keptCursor--
	}
	cursor := &keptCursor
	if floor == "absent" {
		cursor = nil
	}
	seedReplaceWakeState(t, s, "kept-host", cursor)
	keptMark := kept
	if floor == "below" {
		node, readErr := s.GetNode(t.Context(), "PROJ-1")
		require.NoError(t, readErr)
		notes := append(node.Annotations, model.Annotation{ID: "pending-survivor", Author: "sender", Text: "pending", Addressee: "worker", CreatedAt: node.CreatedAt.Add(time.Second)})
		require.NoError(t, s.SetAnnotations(t.Context(), node.ID, notes))
		kept, err = s.JournalTail(t.Context())
		require.NoError(t, err)
	}
	addressTo(t, s, "worker", "PROJ-2")
	dropped, err := s.JournalTail(t.Context())
	require.NoError(t, err)
	droppedCursor := map[string]int64{"above": dropped, "below": kept - 1, "tail": kept}[floor]
	if cursor != nil {
		cursor = &droppedCursor
	}
	seedReplaceWakeState(t, s, "removed-host", cursor)
	data, err := s.Export(t.Context(), "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-2")
	if empty {
		data = withoutExportNode(data, "PROJ-1")
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return wakeReplaceState{store: s, raw: raw, data: data, kept: kept, dropped: dropped, keptMark: keptMark,
		cursor: cursor != nil, keptCursor: keptCursor, droppedCursor: droppedCursor}
}

// wakeReplaceRows serializes every selected column, including future additive metadata.
func wakeReplaceRows(t *testing.T, raw *sql.DB, query string, omit []string, args ...any) []string {
	t.Helper()
	rows, err := raw.QueryContext(t.Context(), query, args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err)
	var result []string
	for rows.Next() {
		values, destinations := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		require.NoError(t, rows.Scan(destinations...))
		row := map[string]any{}
		for i, column := range columns {
			if !slices.Contains(omit, column) {
				row[column] = values[i]
			}
		}
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		result = append(result, string(encoded))
	}
	require.NoError(t, rows.Err())
	return result
}

func wakeReplaceMetadata(t *testing.T, raw *sql.DB) [][]string {
	t.Helper()
	return [][]string{
		wakeReplaceRows(t, raw, `SELECT * FROM agent_registrations ORDER BY agent_id,host_id,uid`, []string{"high_water"}),
		wakeReplaceRows(t, raw, `SELECT * FROM wake_leases ORDER BY agent_id,host_id,uid`, []string{"high_water"}),
		wakeReplaceRows(t, raw, `SELECT * FROM wake_scan_cursor ORDER BY host_id,uid`, []string{"cursor"}),
		wakeReplaceRows(t, raw, `SELECT * FROM wake_budget_counters ORDER BY agent_id,host_id,uid`, nil),
		wakeReplaceRows(t, raw, `SELECT * FROM meta WHERE key LIKE 'wake%' ORDER BY key`, nil),
	}
}

func assertReplaceWakeMarks(t *testing.T, f wakeReplaceState, tail int64) {
	t.Helper()
	for _, item := range []struct {
		host   string
		mark   int64
		cursor int64
	}{{"kept-host", f.keptMark, f.keptCursor}, {"removed-host", f.dropped, f.droppedCursor}} {
		var registration, lease, dirty int64
		require.NoError(t, f.raw.QueryRow(`SELECT high_water FROM agent_registrations WHERE host_id=?`, item.host).Scan(&registration))
		require.NoError(t, f.raw.QueryRow(`SELECT high_water,dirty FROM wake_leases WHERE host_id=?`, item.host).Scan(&lease, &dirty))
		assert.Equal(t, min(item.mark, tail), registration)
		assert.Equal(t, min(item.mark, tail), lease)
		assert.Equal(t, int64(1), dirty, "reconciliation preserves the live dirty lease")
		if f.cursor {
			got, err := f.store.WakeScanCursor(t.Context(), item.host, "1000")
			require.NoError(t, err)
			assert.Equal(t, min(item.cursor, tail), got)
		}
	}
}

func TestWakeReplace_TrimsOnlyRemovedJournalBookkeeping(t *testing.T) {
	for _, tt := range []struct {
		name, floor string
		empty       bool
	}{{"above tail", "above", false}, {"below tail", "below", false}, {"at tail", "tail", false}, {"absent cursor", "absent", false}, {"empty journal", "above", true}} {
		t.Run(tt.name, func(t *testing.T) {
			f := newWakeReplaceState(t, tt.floor, tt.empty)
			tail := f.kept
			if tt.empty {
				tail = 0
			}
			metadata := wakeReplaceMetadata(t, f.raw)
			ledger := wakeReplaceRows(t, f.raw, `SELECT * FROM wake_ledger WHERE event_seq<=? ORDER BY agent_id,host_id,uid,event_seq,epoch`, nil, tail)
			_, err := f.store.Import(t.Context(), f.data, sqlite.ImportModeReplace, tt.empty)
			require.NoError(t, err)
			actual, err := f.store.JournalTail(t.Context())
			require.NoError(t, err)
			require.Equal(t, tail, actual)
			assert.Equal(t, ledger, wakeReplaceRows(t, f.raw, `SELECT * FROM wake_ledger ORDER BY agent_id,host_id,uid,event_seq,epoch`, nil), "all surviving ledger columns are unchanged; removed claims and terminals are gone")
			assert.Equal(t, metadata, wakeReplaceMetadata(t, f.raw), "routing, epochs, holds, ownership and budget accounting are unchanged")
			assertReplaceWakeMarks(t, f, tail)
		})
	}
}

func wakeReplaceAllState(t *testing.T, raw *sql.DB) [][]string {
	t.Helper()
	var state [][]string
	for _, query := range []string{
		`SELECT * FROM sync_events ORDER BY rowid`,
		`SELECT * FROM wake_ledger ORDER BY agent_id,host_id,uid,event_seq,epoch`,
		`SELECT * FROM wake_scan_cursor ORDER BY host_id,uid`,
		`SELECT * FROM agent_registrations ORDER BY agent_id,host_id,uid`,
		`SELECT * FROM wake_leases ORDER BY agent_id,host_id,uid`,
		`SELECT * FROM wake_budget_counters ORDER BY agent_id,host_id,uid`,
		`SELECT * FROM wake_outcome_rejections ORDER BY agent_id,host_id,uid,event_seq,epoch,attempt_token,outcome`,
		`SELECT * FROM agent_inbox_cursor ORDER BY agent_id`,
		`SELECT * FROM hook_dispatch_cursor ORDER BY id`,
		`SELECT * FROM hook_dispatch_ledger ORDER BY hook_name,event_seq`,
		`SELECT * FROM meta ORDER BY key`,
	} {
		state = append(state, wakeReplaceRows(t, raw, query, nil))
	}
	return state
}

func TestWakeReplace_CleanupFailure_RollsBackJournalAndLocalState(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"ledger", `CREATE TRIGGER refuse_wake_trim BEFORE DELETE ON wake_ledger BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
		{"cursor", `CREATE TRIGGER refuse_wake_trim BEFORE UPDATE ON wake_scan_cursor BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
		{"registration", `CREATE TRIGGER refuse_wake_trim BEFORE UPDATE ON agent_registrations BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
		{"lease", `CREATE TRIGGER refuse_wake_trim BEFORE UPDATE ON wake_leases BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			require.NoError(t, f.store.InboxAckThrough(t.Context(), "worker", f.dropped))
			_, err := f.raw.Exec(`INSERT INTO hook_dispatch_cursor(id,cursor) VALUES(1,?)`, f.dropped)
			require.NoError(t, err)
			_, err = f.store.ClaimHookDispatch(t.Context(), "rollback-hook", f.dropped, time.Minute)
			require.NoError(t, err)
			before := wakeReplaceAllState(t, f.raw)
			board, err := f.store.Export(t.Context(), "", "")
			require.NoError(t, err)
			_, err = f.raw.Exec(tt.trigger)
			require.NoError(t, err)
			_, err = f.store.Import(t.Context(), f.data, sqlite.ImportModeReplace, false)
			require.ErrorContains(t, err, "forced wake cleanup failure")
			assert.Equal(t, before, wakeReplaceAllState(t, f.raw), "all journal and wake row bytes are restored by rollback")
			after, err := f.store.Export(t.Context(), "", "")
			require.NoError(t, err)
			assert.Equal(t, board.Nodes, after.Nodes)
			assert.Equal(t, board.Dependencies, after.Dependencies)
		})
	}
}
