// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-76, the hook-dispatch half of the MTIX-66 defect. hook_dispatch_cursor
// (the scan floor) and hook_dispatch_ledger (the per-(hook,event) claim) are
// both keyed by sync_events.rowid, which DiscardLocal resets to 1 by wiping a
// table that has no AUTOINCREMENT. A surviving floor sits above the whole new
// journal and every floor advance is monotonic (MAX), so it can never come
// back down on its own: the dispatcher stops seeing events entirely, and no
// hook fires again until the journal grows past the old tail. Nothing errors
// and nothing logs — the wake path just goes quiet.

// TestDiscardLocal_StaleHookFloorDoesNotHideNewEvents pins both halves: the
// floor must not outlive the journal it indexes into, and a stale ledger row
// must not pre-claim the unrelated event that later takes its rowid.
func TestDiscardLocal_StaleHookFloorDoesNotHideNewEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mtixDir := discardMtixDir(t)

	addressThree(t, s, "opus")
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.Greater(t, tail, int64(0), "the three writes must have journaled something")

	// Bring the dispatcher fully up to date: floor at the tail, and every seq
	// in the journal terminally dispatched for hook "wake". Claiming the whole
	// range is what guarantees a rowid collision after the reset, whatever
	// rowid the next insert happens to take.
	for seq := int64(1); seq <= tail; seq++ {
		claimed, claimErr := s.ClaimHookDispatch(ctx, "wake", seq, time.Minute)
		require.NoError(t, claimErr)
		require.True(t, claimed)
		require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", seq, "delivered"))
	}
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, tail))
	preFloor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, tail, preFloor, "premise: the floor is at the old tail before the discard")

	require.NoError(t, sqlite.DiscardLocal(ctx, s, mtixDir))

	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	require.Zero(t, floor,
		"the scan floor must not survive the journal it indexes into; floor advances are monotonic, so a stale one never recovers")

	// Everything below happens AFTER the discard and must be dispatchable.
	addressTo(t, s, "opus", "PROJ-A")
	newTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.LessOrEqual(t, newTail, tail,
		"guard the premise: rowids must have restarted at or below the old tail, or this proves nothing")

	events, err := s.ReadJournalSince(ctx, floor, 100)
	require.NoError(t, err)
	require.NotEmpty(t, events,
		"an event journaled after DiscardLocal must be visible to the dispatcher")

	claimed, err := s.ClaimHookDispatch(ctx, "wake", events[0].Seq, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed,
		"a post-discard event must be claimable; a stale ledger row pre-claims the unrelated event that reuses its rowid")
}

// TestDiscardLocal_ThenBootstrapPull_SeedsFloorAtNewTail: clearing the floor
// must not swing the failure the other way. A pull straight after a discard
// refills the journal with HISTORY, and history must arrive pre-dispatched —
// the FR-20 §8 bootstrap rule that a fresh clone never fires a hook backlog
// storm. That rule is enforced by re-seeding the floor at the tail when the
// pre-pull journal was empty, which a discard makes true; but the re-seed is
// monotonic, so it was a silent no-op while a stale higher floor survived.
func TestDiscardLocal_ThenBootstrapPull_SeedsFloorAtNewTail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mtixDir := discardMtixDir(t)

	addressThree(t, s, "opus")
	oldTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, oldTail))

	require.NoError(t, sqlite.DiscardLocal(ctx, s, mtixDir))

	// Stand in for the pull that follows a discard: the journal refills with
	// events the agent never saw dispatched, and must not be woken for them.
	addressTo(t, s, "opus", "PROJ-A")
	addressTo(t, s, "opus", "PROJ-B")
	newTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.Less(t, newTail, oldTail,
		"guard the premise: the refilled journal must be SHORTER than the old one, so a surviving floor would mask the re-seed")

	require.NoError(t, s.InitHookScanFloorAtTail(ctx))

	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, newTail, floor,
		"the bootstrap re-seed must land on the NEW tail; a stale higher floor makes this monotonic call a silent no-op")

	events, err := s.ReadJournalSince(ctx, floor, 100)
	require.NoError(t, err)
	require.Empty(t, events, "pulled history must arrive pre-dispatched, not as a hook backlog")
}
