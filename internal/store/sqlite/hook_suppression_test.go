// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the transactional lifetime of local FR-19 workflow suppression.
package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// suppressionFixture returns a store whose TEST-1 node was claimed and then
// finished locally, so any foreign workflow event stamped below the local
// transition loses at ingest. It also returns the loser lamport stamp.
func suppressionFixture(t *testing.T) (*sqlite.Store, *sql.DB, int64) {
	t.Helper()
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	_, err := raw.Exec(`UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`, "aaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	mustCreateNode(t, s, "TEST-1", "")
	markEventsPushed(t, raw)
	require.NoError(t, s.ClaimNode(ctx, "TEST-1", "agent-a"))
	require.NoError(t, s.TransitionStatus(ctx, "TEST-1", model.StatusDone, "finished", "agent-a"))
	return s, raw, latestLamport(t, raw, model.OpTransitionStatus) - 1
}

func losingEvent(t *testing.T, op model.OpType, payload any, lamport int64, eventID string) *model.SyncEvent {
	t.Helper()
	e := foreignWorkflowEvent(t, "TEST-1", op, payload, lamport, eventID)
	e.ProjectPrefix = "TEST"
	return e
}

func assertSuppressionCount(t *testing.T, raw *sql.DB, want int) {
	t.Helper()
	var count int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM hook_suppressed_events`).Scan(&count))
	require.Equal(t, want, count)
}

// TestReadJournalSince_LosingWorkflowEventOfEveryType_Skipped covers every
// workflow op type the ingest decision can see, so a rule limited to claims
// fails.
func TestReadJournalSince_LosingWorkflowEventOfEveryType_Skipped(t *testing.T) {
	tests := []struct {
		name    string
		op      model.OpType
		payload any
	}{
		{"claim", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}},
		{"unclaim", model.OpUnclaim, &model.UnclaimPayload{}},
		{"defer", model.OpDefer, &model.DeferPayload{Reason: "later"}},
		{"transition_status", model.OpTransitionStatus,
			&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw, lamport := suppressionFixture(t)
			journal, err := s.ReadJournalSince(ctx, 0, 100)
			require.NoError(t, err)
			cursor := journal[len(journal)-1].Seq
			before := nodeRow(t, raw, "TEST-1")
			loser := losingEvent(t, tt.op, tt.payload, lamport, lowEventID)
			pullEvents(t, s, []*model.SyncEvent{loser})
			require.Equal(t, before, nodeRow(t, raw, "TEST-1"), "a loser must change no node column")
			journal, err = s.ReadJournalSince(ctx, cursor, 100)
			require.NoError(t, err)
			require.Empty(t, journal, "losing %s must not report a state change to hooks", tt.name)
			assertSuppressionCount(t, raw, 1)
			var retained int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, loser.EventID).Scan(&retained))
			require.Equal(t, 1, retained, "the mirrored event stays as sync evidence")
		})
	}
}

// snapshotApplyTables captures every table one replay writes, so a rollback
// test fails if any of them is written outside the apply transaction.
func snapshotApplyTables(t *testing.T, raw *sql.DB, nodeID string) map[string]any {
	t.Helper()
	dump := func(query string) []string {
		rows, err := raw.Query(query)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var a, b string
			require.NoError(t, rows.Scan(&a, &b))
			out = append(out, a+"="+b)
		}
		require.NoError(t, rows.Err())
		return out
	}
	return map[string]any{
		// Mirror, dedupe ledger, and every meta key (lamport, vector clock, hub mark).
		"sync_events":    dump(`SELECT event_id, sync_status FROM sync_events ORDER BY rowid`),
		"applied_events": dump(`SELECT event_id, event_id FROM applied_events ORDER BY event_id`),
		"meta":           dump(`SELECT key, value FROM meta ORDER BY key`),
		"node":           nodeRow(t, raw, nodeID),
	}
}

// TestIdempotentApply_MarkerWriteFails_RollsBackReplay breaks the marker table
// and requires the whole replay to roll back, not only the mirror row.
func TestIdempotentApply_MarkerWriteFails_RollsBackReplay(t *testing.T) {
	ctx := context.Background()
	s, raw, lamport := suppressionFixture(t)
	_, err := raw.Exec(`DROP TABLE hook_suppressed_events`)
	require.NoError(t, err)
	before := snapshotApplyTables(t, raw, "TEST-1")
	loser := losingEvent(t, model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, lamport, highEventID)
	err = s.WithTx(ctx, func(tx *sql.Tx) error { return s.IdempotentApply(ctx, tx, loser) })
	require.ErrorContains(t, err, "suppress losing workflow hook")
	require.Equal(t, before, snapshotApplyTables(t, raw, "TEST-1"),
		"a failed marker write must leave no mirror, dedupe row, clock, hub mark or node change")
}

func TestIdempotentApply_AbortedTransaction_LeavesNoMarker(t *testing.T) {
	ctx := context.Background()
	s, raw, lamport := suppressionFixture(t)
	loser := losingEvent(t, model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, lamport, lowEventID)
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		require.NoError(t, s.IdempotentApply(ctx, tx, loser))
		return model.ErrInvalidInput // abort after the suppression decision
	})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assertSuppressionCount(t, raw, 0)
}

func TestReadJournalSince_ReopenedStore_KeepsSkipDecision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.WriteDB().Exec(`UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`, "aaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	mustCreateNode(t, s, "TEST-1", "")
	markEventsPushed(t, s.WriteDB())
	require.NoError(t, s.ClaimNode(ctx, "TEST-1", "agent-a"))
	require.NoError(t, s.TransitionStatus(ctx, "TEST-1", model.StatusDone, "finished", "agent-a"))
	loser := losingEvent(t, model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, 1, lowEventID)
	pullEvents(t, s, []*model.SyncEvent{loser})
	assertSuppressionCount(t, s.WriteDB(), 1)
	require.NoError(t, s.Close())
	s, err = sqlite.New(path, slog.Default())
	require.NoError(t, err)
	assertSuppressionCount(t, s.WriteDB(), 1)
	var seq int64
	require.NoError(t, s.WriteDB().QueryRow(`SELECT rowid FROM sync_events WHERE event_id = ?`, loser.EventID).Scan(&seq))
	journal, err := s.ReadJournalSince(ctx, seq-1, 1)
	require.NoError(t, err)
	require.Empty(t, journal, "restart must preserve the skip decision")
}

func TestHookSuppression_DiscardedJournalEvent_RemovesMarker(t *testing.T) {
	s, _, lamport := suppressionFixture(t)
	loser := losingEvent(t, model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, lamport, lowEventID)
	pullEvents(t, s, []*model.SyncEvent{loser})
	// The store's own write connection enforces foreign keys, so the cascade runs.
	assertSuppressionCount(t, s.WriteDB(), 1)
	_, err := s.WriteDB().Exec(`DELETE FROM sync_events WHERE event_id = ?`, loser.EventID)
	require.NoError(t, err)
	assertSuppressionCount(t, s.WriteDB(), 0)
}

func TestNew_ExistingStoreWithoutMarkerTable_CreatesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	_, err = s.WriteDB().Exec(`DROP TABLE hook_suppressed_events`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assertSuppressionCount(t, s.WriteDB(), 0)
}

// TestReadJournalSince_MarkerTableMissing_ReturnsUnfilteredEvents covers a
// store opened read-only on an older database: it has no decisions, so the
// read must work and show every event instead of failing.
func TestReadJournalSince_MarkerTableMissing_ReturnsUnfilteredEvents(t *testing.T) {
	ctx := context.Background()
	s, raw, _ := suppressionFixture(t)
	_, err := raw.Exec(`DROP TABLE hook_suppressed_events`)
	require.NoError(t, err)
	journal, err := s.ReadJournalSince(ctx, 0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, journal)
}

// TestJournal_LosingClaimAfterLocalDone_Skipped verifies FR-19 exposes only
// workflow events that won at ingest, retaining sync evidence and pagination.
func TestJournal_LosingClaimAfterLocalDone_Skipped(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	_, err := raw.Exec(`UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`, "aaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	mustCreateNode(t, s, "TEST-1", "")
	markEventsPushed(t, raw)
	require.NoError(t, s.ClaimNode(ctx, "TEST-1", "agent-a"))
	require.NoError(t, s.TransitionStatus(ctx, "TEST-1", model.StatusDone, "finished", "agent-a"))
	before := nodeRow(t, raw, "TEST-1")
	journal, err := s.ReadJournalSince(ctx, 0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, journal)
	cursor := journal[len(journal)-1].Seq
	lamport := latestLamport(t, raw, model.OpTransitionStatus)
	loser := foreignWorkflowEvent(t, "TEST-1", model.OpClaim,
		&model.ClaimPayload{AgentID: "agent-b"}, lamport-1, lowEventID)
	loser.ProjectPrefix = "TEST"
	pullEvents(t, s, []*model.SyncEvent{loser, loser})
	require.Equal(t, before, nodeRow(t, raw, "TEST-1"))
	for _, table := range []string{"sync_events", "applied_events"} {
		var count int
		// Fixed queries keep both evidence checks parameterized.
		query := `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`
		if table == "applied_events" {
			query = `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`
		}
		require.NoError(t, raw.QueryRow(query, loser.EventID).Scan(&count))
		require.Equal(t, 1, count, table)
	}
	journal, err = s.ReadJournalSince(ctx, cursor, 1)
	require.NoError(t, err)
	require.Empty(t, journal, "losing claim must not report a status change to hooks")
	winner := foreignWorkflowEvent(t, "TEST-1", model.OpClaim,
		&model.ClaimPayload{AgentID: "agent-c"}, lamport+1, highEventID)
	winner.ProjectPrefix = "TEST"
	pullEvents(t, s, []*model.SyncEvent{winner})
	journal, err = s.ReadJournalSince(ctx, cursor, 1)
	require.NoError(t, err)
	require.Len(t, journal, 1, "skipping a loser must not starve a later winner")
	require.Equal(t, winner.EventID, journal[0].EventID)
	require.True(t, journal[0].Synced)
	require.Greater(t, journal[0].Seq, cursor+1)
}

// TestReadJournalSince_MarkerTableUnreadable_ReturnsErrorAndNoEvents requires
// that only an absent table selects the unfiltered path: a table that exists
// but cannot be read must fail the call, never silently show losing events.
func TestReadJournalSince_MarkerTableUnreadable_ReturnsErrorAndNoEvents(t *testing.T) {
	ctx := context.Background()
	s, raw, _ := suppressionFixture(t)
	_, err := raw.Exec(`DROP TABLE hook_suppressed_events`)
	require.NoError(t, err)
	// Same name, wrong shape: the table exists, but the skip query cannot read it.
	_, err = raw.Exec(`CREATE TABLE hook_suppressed_events (wrong TEXT)`)
	require.NoError(t, err)
	journal, err := s.ReadJournalSince(ctx, 0, 100)
	require.Error(t, err)
	require.Empty(t, journal)
}
