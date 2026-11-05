// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the read boundary and missing identities exposed by round-one review.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookFence_FloorReadBeforeReplaceCannotSkipUnseenEvents(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	addressTo(t, s, "worker", "PROJ-2")
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, tail))
	observed, err := s.HookCursor(ctx)
	require.NoError(t, err)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	surviving, err := s.JournalTail(ctx)
	require.NoError(t, err)
	for _, id := range []string{"PROJ-3", "PROJ-4", "PROJ-5"} {
		addressTo(t, s, "worker", id)
	}
	page, err := s.ReadJournalSince(ctx, observed, 500)
	require.NoError(t, err)
	require.NotEmpty(t, page)
	require.Greater(t, page[0].Seq, surviving+1, "the stale floor omitted freshly reused sequences")
	misses, err := s.AdvanceHookScanFloorForEvents(ctx, observed, page)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), misses)
	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, surviving, floor, "a page read from a rewound floor cannot consume events it omitted")
	unseen, err := s.ReadJournalSince(ctx, floor, 500)
	require.NoError(t, err)
	require.NotEmpty(t, unseen)
	assert.Equal(t, surviving+1, unseen[0].Seq)
}

func TestHookFence_AbsentSnapshotRowsCannotAdvanceDroppedTail(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	addressTo(t, s, "worker", "PROJ-2")
	snapshot, err := s.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	misses, err := s.AdvanceHookScanFloorForEvents(ctx, 0, snapshot)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), misses)
	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, tail, floor)
}

func TestHookFence_EmptyIdentityCannotTouchPresentClaim(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	events, err := s.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	event := events[len(events)-1]
	won, matched, err := s.ClaimHookDispatchEvent(ctx, "fenced", event, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.True(t, matched)
	backdateClaim(t, s, "fenced", event.Seq, time.Hour)
	before := hookReplaceBookkeeping(t, s.ReadDB())
	empty := sqlite.JournalEvent{Seq: event.Seq}
	won, matched, err = s.ClaimHookDispatchEvent(ctx, "fenced", empty, time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	assert.False(t, matched)
	matched, err = s.RecordHookDispatchEventOutcome(ctx, "fenced", empty, sqlite.OutcomeError)
	require.NoError(t, err)
	assert.False(t, matched)
	assert.Equal(t, before, hookReplaceBookkeeping(t, s.ReadDB()))
}

func TestHookFence_InboxIdentityAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	events, err := s.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	event := events[len(events)-1]
	for _, hook := range []string{"first-hook", "second-hook"} {
		matched, err := s.RecordInboxDeliveryEvent(ctx, "recipient", event, hook)
		require.NoError(t, err)
		require.True(t, matched)
	}
	var count int
	require.NoError(t, s.ReadDB().QueryRow(`SELECT COUNT(*) FROM inbox_deliveries WHERE agent_id='recipient' AND event_seq=?`, event.Seq).Scan(&count))
	assert.Equal(t, 1, count)
	matched, err := s.RecordInboxDeliveryEvent(ctx, "recipient", sqlite.JournalEvent{Seq: event.Seq + 1}, "empty")
	require.NoError(t, err)
	assert.False(t, matched)
	require.NoError(t, s.ReadDB().QueryRow(`SELECT COUNT(*) FROM inbox_deliveries WHERE event_seq>?`, event.Seq).Scan(&count))
	assert.Zero(t, count)
}

func TestHookFence_MissingClaimCleanupIsDeleteOnly(t *testing.T) {
	for _, state := range []string{"expired", "live", "terminal", "refused"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s, _ := retentionStore(t)
			addressTo(t, s, "worker", "PROJ-1")
			addressTo(t, s, "worker", "PROJ-2")
			event, ok, err := s.ReadJournalEventAt(ctx, 1)
			require.NoError(t, err)
			require.True(t, ok)
			won, matched, err := s.ClaimHookDispatchEvent(ctx, "missing", event, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			require.True(t, matched)
			if state != "live" {
				backdateClaim(t, s, "missing", event.Seq, time.Hour)
			}
			if state == "terminal" {
				matched, err = s.RecordHookDispatchEventOutcome(ctx, "missing", event, sqlite.OutcomeDelivered)
				require.NoError(t, err)
				require.True(t, matched)
				backdateClaim(t, s, "missing", event.Seq, time.Hour)
			}
			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			data = withoutExportNode(data, "PROJ-1")
			require.NoError(t, sqlite.RecomputeExportChecksum(data))
			_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
			require.NoError(t, err)
			before := hookReplaceBookkeeping(t, s.ReadDB())
			if state == "refused" {
				_, err = s.WriteDB().Exec(`CREATE TRIGGER refuse_missing_delete BEFORE DELETE ON hook_dispatch_ledger BEGIN SELECT RAISE(ABORT,'missing cleanup refused'); END`)
				require.NoError(t, err)
			}
			won, matched, err = s.ClaimHookDispatchEvent(ctx, "missing", sqlite.JournalEvent{Seq: event.Seq}, time.Minute)
			assert.False(t, won, "absence never grants ownership or inserts a claim")
			if state == "refused" {
				require.ErrorContains(t, err, "missing cleanup refused")
				assert.Equal(t, before, hookReplaceBookkeeping(t, s.ReadDB()))
				return
			}
			require.NoError(t, err)
			if state == "expired" {
				assert.True(t, matched)
				var count int
				require.NoError(t, s.ReadDB().QueryRow(`SELECT COUNT(*) FROM hook_dispatch_ledger`).Scan(&count))
				assert.Zero(t, count)
				won, matched, err = s.ClaimHookDispatchEvent(ctx, "missing", sqlite.JournalEvent{Seq: event.Seq}, time.Minute)
				require.NoError(t, err)
				assert.False(t, won)
				assert.False(t, matched)
			} else {
				assert.False(t, matched)
				assert.Equal(t, before, hookReplaceBookkeeping(t, s.ReadDB()))
			}
		})
	}
}
