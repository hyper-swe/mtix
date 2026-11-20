// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-130.20.3: hook, inbox-delivery and wake ledger rows are local
// bookkeeping. Their commits must not run the on-commit callbacks, which
// record an export request and trigger the exporter, hook dispatch and hub
// contact; the daemon's own ticks write these rows on an idle board.
func TestHookBookkeepingWrites_DoNotRunOnCommitCallbacks(t *testing.T) {
	s, now := reclaimStore(t)
	seedExportedRows(t, s)
	events, err := s.ReadJournalSince(t.Context(), 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, events, "the seeded write must leave a journal event to dispatch")
	ev := events[0]
	ctx := t.Context()
	// A stale claim on a journal row that does not exist: the missing-claim discard removes it.
	won, err := s.ClaimHookDispatch(ctx, "gone", 99, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	*now = now.Add(2 * time.Minute)
	var calls int
	s.SetOnCommit(func() { calls++ })
	tests := []struct {
		name  string
		do    func() error
		table string // a table the write must change, proving it ran; empty when it may legitimately change nothing
	}{
		{"claim dispatch", func() error { _, err := s.ClaimHookDispatch(ctx, "h", 2, time.Minute); return err }, "hook_dispatch_ledger"},
		{"claim dispatch event", func() error { _, _, err := s.ClaimHookDispatchEvent(ctx, "h", ev, time.Minute); return err }, "hook_dispatch_ledger"},
		{"discard missing claim (empty event id)", func() error {
			removed, _, err := s.ClaimHookDispatchEvent(ctx, "gone", sqlite.JournalEvent{Seq: 99}, time.Minute)
			_ = removed
			return err
		}, "hook_dispatch_ledger"},
		{"record outcome", func() error {
			if err := s.RecordHookDispatchOutcome(ctx, "h", 1, "ok"); err != nil {
				return err
			}
			return s.RecordHookDispatchOutcome(ctx, "h", 2, "ok")
		}, "hook_dispatch_ledger"},
		{"advance floor for events", func() error { _, err := s.AdvanceHookScanFloorForEvents(ctx, 0, events); return err }, "hook_dispatch_cursor"},
		{"advance cursor", func() error { return s.AdvanceHookCursor(ctx, 5) }, "hook_dispatch_cursor"},
		{"record inbox delivery", func() error { return s.RecordInboxDelivery(ctx, "a", 1, "h") }, "inbox_deliveries"},
		{"write hook log", func() error { return s.WriteHookLog(ctx, sqlite.HookLogEntry{Hook: "h", Outcome: "ok"}) }, "hook_log"},
		{"inbox delivery event", func() error { _, err := s.RecordInboxDeliveryEvent(ctx, "b", ev, "h"); return err }, "inbox_deliveries"},
		{"advance floor clamped", func() error { return s.AdvanceHookScanFloorClamped(ctx, 1) }, ""},
		{"prune ledger", func() error { return s.PruneHookDispatchLedger(ctx) }, ""},
		{"init floor at tail", func() error { return s.InitHookScanFloorAtTail(ctx) }, ""},
		{"claim wake", func() error {
			_, _, err := s.ClaimWake(ctx, model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 1, Epoch: 1}, time.Minute)
			return err
		}, "wake_ledger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, rows, tableBefore := calls, exportedRows(t, s), tableDump(t, s, tt.table)
			require.NoError(t, tt.do())
			require.Equal(t, before, calls, "bookkeeping commit ran the on-commit callbacks")
			require.Equal(t, rows, exportedRows(t, s), "bookkeeping commit changed exported content")
			if tt.table != "" {
				require.NotEqual(t, tableBefore, tableDump(t, s, tt.table), "the write did not land in %s", tt.table)
			}
		})
	}
}

// seedExportedRows puts a node (and so a journal event) in every store the
// bookkeeping writes must leave alone.
func seedExportedRows(t *testing.T, s *sqlite.Store) {
	t.Helper()
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	node := &model.Node{ID: "T-1", Project: "T", Seq: 1, Title: "seed", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeIssue, CreatedAt: at, UpdatedAt: at}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, s.CreateNode(t.Context(), node))
}

// exportedRows dumps every row of the tables the board export and the sync
// journal read, so a test can prove a write left exported content alone.
func exportedRows(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	var out string
	for _, table := range []string{"nodes", "dependencies", "agents", "sessions", "task_history", "sync_events"} {
		out += dumpTable(t, s, table)
	}
	return out
}

func dumpTable(t *testing.T, s *sqlite.Store, table string) string {
	t.Helper()
	// Test-only: fixed table names from the literal list in exportedRows, never input.
	rows, err := s.WriteDB().QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1")
	require.NoError(t, err, table)
	defer func() { require.NoError(t, rows.Close()) }()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		out += fmt.Sprintf("%s:%v\n", table, vals)
	}
	require.NoError(t, rows.Err())
	return out
}
