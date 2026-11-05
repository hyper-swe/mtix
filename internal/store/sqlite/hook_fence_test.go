// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests each dispatcher write resuming with a pre-replace event identity.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type hookFenceFixture struct {
	s             *sqlite.Store
	old, fresh    sqlite.JournalEvent
	snapshot      []sqlite.JournalEvent
	survivingTail int64
}

func replacedHookEvent(t *testing.T, claimed bool) hookFenceFixture {
	t.Helper()
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	addressTo(t, s, "worker", "PROJ-2")
	events, err := s.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	old := events[len(events)-1]
	if claimed {
		won, err := s.ClaimHookDispatch(ctx, "fenced", old.Seq, time.Minute)
		require.NoError(t, err)
		require.True(t, won)
	}
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	addressTo(t, s, "worker", "PROJ-3")
	fresh, ok, err := s.ReadJournalEventAt(ctx, old.Seq)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, old.EventID, fresh.EventID)
	return hookFenceFixture{s: s, old: old, fresh: fresh, snapshot: events, survivingTail: tail}
}

func TestHookFence_ClaimResumesAfterReplace(t *testing.T) {
	f := replacedHookEvent(t, false)
	ctx := context.Background()
	won, matched, err := f.s.ClaimHookDispatchEvent(ctx, "fenced", f.old, time.Minute)
	require.NoError(t, err)
	assert.False(t, matched)
	assert.False(t, won)
	won, matched, err = f.s.ClaimHookDispatchEvent(ctx, "fenced", f.fresh, time.Minute)
	require.NoError(t, err)
	require.True(t, matched)
	require.True(t, won)
	// A stale old identity cannot reclaim a new event's expired claim either.
	backdateClaim(t, f.s, "fenced", f.fresh.Seq, time.Hour)
	before := hookReplaceBookkeeping(t, f.s.ReadDB())
	won, matched, err = f.s.ClaimHookDispatchEvent(ctx, "fenced", f.old, time.Minute)
	require.NoError(t, err)
	assert.False(t, matched)
	assert.False(t, won)
	assert.Equal(t, before, hookReplaceBookkeeping(t, f.s.ReadDB()))
}

func TestHookFence_OutcomeResumesAfterReplace(t *testing.T) {
	f := replacedHookEvent(t, true)
	ctx := context.Background()
	won, matched, err := f.s.ClaimHookDispatchEvent(ctx, "fenced", f.fresh, time.Minute)
	require.NoError(t, err)
	require.True(t, matched)
	require.True(t, won)
	before := hookReplaceBookkeeping(t, f.s.ReadDB())
	matched, err = f.s.RecordHookDispatchEventOutcome(ctx, "fenced", f.old, sqlite.OutcomeDelivered)
	require.NoError(t, err)
	assert.False(t, matched)
	assert.Equal(t, before, hookReplaceBookkeeping(t, f.s.ReadDB()), "old completion cannot settle the new event's claim")
	matched, err = f.s.RecordHookDispatchEventOutcome(ctx, "fenced", f.fresh, sqlite.OutcomeDelivered)
	require.NoError(t, err)
	require.True(t, matched)
	var outcome string
	require.NoError(t, f.s.ReadDB().QueryRow(`SELECT outcome FROM hook_dispatch_ledger WHERE hook_name='fenced' AND event_seq=?`, f.fresh.Seq).Scan(&outcome))
	assert.Equal(t, sqlite.OutcomeDelivered, outcome)
}

func TestHookFence_FloorResumesAtLastMatchingPrefix(t *testing.T) {
	f := replacedHookEvent(t, false)
	ctx := context.Background()
	misses, err := f.s.AdvanceHookScanFloorForEvents(ctx, 0, f.snapshot)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), misses)
	floor, err := f.s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.survivingTail, floor)
	fresh, err := f.s.ReadJournalSince(ctx, floor, 500)
	require.NoError(t, err)
	misses, err = f.s.AdvanceHookScanFloorForEvents(ctx, 0, fresh)
	require.NoError(t, err)
	assert.Zero(t, misses)
	floor, err = f.s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.fresh.Seq, floor)
	before := hookReplaceBookkeeping(t, f.s.ReadDB())
	misses, err = f.s.AdvanceHookScanFloorForEvents(ctx, 0, nil)
	require.NoError(t, err)
	assert.Zero(t, misses)
	assert.Equal(t, before, hookReplaceBookkeeping(t, f.s.ReadDB()))
}

func TestHookFence_SurvivingEventCanFinishAfterReplace(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "worker", "PROJ-1")
	old, err := s.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	survivor := old[len(old)-1]
	won, matched, err := s.ClaimHookDispatchEvent(ctx, "survivor", survivor, time.Minute)
	require.NoError(t, err)
	require.True(t, matched)
	require.True(t, won)
	addressTo(t, s, "worker", "PROJ-2")
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	matched, err = s.RecordHookDispatchEventOutcome(ctx, "survivor", survivor, sqlite.OutcomeDelivered)
	require.NoError(t, err)
	assert.True(t, matched)
	misses, err := s.AdvanceHookScanFloorForEvents(ctx, 0, old)
	require.NoError(t, err)
	assert.Zero(t, misses)
	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, survivor.Seq, floor)
}
