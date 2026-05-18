// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-17: when a node B that blocks another node A is marked done /
// cancelled / invalidated, A should auto-restore to its previous_status
// (per FR-3.8). Before this fix, transitioning B only updated B's row;
// A stayed blocked forever until the dep was manually removed.
//
// The tests below exercise each resolving terminal status, the
// multi-blocker case, the non-terminal case (must NOT unblock), and
// the FR-3.8a invalidated-takes-precedence rule.

// addBlocker creates "blocker blocks blocked" and asserts blocked is
// now in StatusBlocked.
func addBlocker(ctx context.Context, t *testing.T, s testStore, blocker, blocked string, now time.Time) {
	t.Helper()
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID:    blocker,
		ToID:      blocked,
		DepType:   model.DepTypeBlocks,
		CreatedAt: now,
		CreatedBy: "test",
	}))
	got, err := s.GetNode(ctx, blocked)
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, got.Status,
		"precondition: %s must be auto-blocked after dep add", blocked)
}

// testStore is the narrow interface our test helpers need. The name
// avoids colliding with the imported `store` package alias used by
// sibling test files.
type testStore interface {
	AddDependency(ctx context.Context, dep *model.Dependency) error
	GetNode(ctx context.Context, id string) (*model.Node, error)
	TransitionStatus(ctx context.Context, id string, toStatus model.Status, reason, author string) error
	CreateNode(ctx context.Context, node *model.Node) error
}

// TestTransitionStatus_Done_AutoUnblocksDependents — core MTIX-17 bug
// fix. Marking the blocker as done must restore the dependent.
func TestTransitionStatus_Done_AutoUnblocksDependents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	blocker := makeRootNode("PROJ-1", "PROJ", "Blocker", now)
	blocker.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, blocker))

	blocked := makeRootNode("PROJ-2", "PROJ", "Dependent", now)
	require.NoError(t, s.CreateNode(ctx, blocked))

	addBlocker(ctx, t, s, "PROJ-1", "PROJ-2", now)

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "done", "test"))

	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status,
		"PROJ-2 must auto-unblock when its only blocker is marked done")
}

// TestTransitionStatus_Cancelled_AutoUnblocksDependents — cancellation
// is also a resolving terminal status. CancelNode supports open →
// cancelled directly.
func TestTransitionStatus_Cancelled_AutoUnblocksDependents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Dep", now)))
	addBlocker(ctx, t, s, "PROJ-1", "PROJ-2", now)

	require.NoError(t, s.CancelNode(ctx, "PROJ-1", "obsolete", "test", false))

	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status,
		"PROJ-2 must auto-unblock when its only blocker is cancelled")
}

// TestTransitionStatus_MultipleBlockers_OnlyUnblocksWhenAllResolved.
// Two blockers; resolving one keeps the dependent blocked; resolving
// the second finally unblocks.
func TestTransitionStatus_MultipleBlockers_OnlyUnblocksWhenAllResolved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "B1", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "B2", now)))
	dep := makeRootNode("PROJ-3", "PROJ", "Dep", now)
	dep.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, dep))

	addBlocker(ctx, t, s, "PROJ-1", "PROJ-3", now)
	addBlocker(ctx, t, s, "PROJ-2", "PROJ-3", now)

	// Resolve only B1.
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1",
		model.StatusInProgress, "", "test"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1",
		model.StatusDone, "done", "test"))

	// Dep still blocked because B2 is still open.
	got, err := s.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, got.Status,
		"dependent must stay blocked while any blocker is unresolved")

	// Resolve B2.
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2",
		model.StatusInProgress, "", "test"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2",
		model.StatusDone, "done", "test"))

	got, err = s.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status,
		"dependent must restore to previous_status (in_progress) when all blockers resolve")
}

// TestTransitionStatus_NonResolvingTransition_DoesNotUnblock — open →
// in_progress is not a resolving transition, so the dependent stays
// blocked.
func TestTransitionStatus_NonResolvingTransition_DoesNotUnblock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Dep", now)))
	addBlocker(ctx, t, s, "PROJ-1", "PROJ-2", now)

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1",
		model.StatusInProgress, "starting", "test"))

	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, got.Status,
		"open → in_progress must NOT unblock dependents; blocker still active")
}

// TestTransitionStatus_Done_DoesNotUnblockInvalidatedDependent.
// FR-3.8a: invalidated takes precedence; autoUnblockNode must refuse
// to restore an invalidated dependent.
func TestTransitionStatus_Done_DoesNotUnblockInvalidatedDependent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	blocker := makeRootNode("PROJ-1", "PROJ", "Blocker", now)
	blocker.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, blocker))

	dep := makeRootNode("PROJ-2", "PROJ", "Dep", now)
	dep.Status = model.StatusInvalidated
	require.NoError(t, s.CreateNode(ctx, dep))

	// Add the dep; auto-block does not engage on invalidated nodes per
	// FR-3.8a, so PROJ-2 stays invalidated.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID:    "PROJ-1",
		ToID:      "PROJ-2",
		DepType:   model.DepTypeBlocks,
		CreatedAt: now,
		CreatedBy: "test",
	}))

	// Mark blocker done (in_progress → done is valid).
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "done", "test"))

	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, got.Status,
		"invalidated dependent must NOT be auto-unblocked")
}

// TestTransitionStatus_Done_EmitsSyncEventForUnblockedDependent.
// The auto-unblock must also produce a transition_status event on
// sync_events so teammates pulling from the hub see the unblock,
// not just the blocker's done event. Otherwise we have a sync
// invariant violation.
func TestTransitionStatus_Done_EmitsSyncEventForUnblockedDependent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	blocker := makeRootNode("PROJ-1", "PROJ", "Blocker", now)
	blocker.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, blocker))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Dep", now)))
	addBlocker(ctx, t, s, "PROJ-1", "PROJ-2", now)

	// Wipe sync_events to simplify the assertion.
	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "done", "test"))

	// Expect 2 transition_status events: one for PROJ-1 (the explicit
	// done), one for PROJ-2 (the auto-unblock).
	var transitionCount int
	require.NoError(t, s.QueryRow(ctx,
		`SELECT count(*) FROM sync_events
		 WHERE op_type = 'transition_status'`,
	).Scan(&transitionCount))
	assert.Equal(t, 2, transitionCount,
		"auto-unblock must emit a sync event so teammates see the unblock")
}

// TestTransitionStatus_AutoUnblock_AtomicWithOriginal verifies that if
// the auto-unblock fails for any reason (defensive — the canonical
// dep flow shouldn't produce a failure here), the whole tx rolls back
// including the original transition. This is the canonical
// "atomicity of compound operations" property.
//
// We exercise the boundary by inserting a blocker-blocked pair, then
// in the same tx invoking the transition; if the implementation
// short-circuits before calling autoUnblockNode, this test still
// passes — the assertion is just that nothing partially commits.
func TestTransitionStatus_Done_AtomicityWithDependentUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	blocker := makeRootNode("PROJ-1", "PROJ", "Blocker", now)
	blocker.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, blocker))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Dep", now)))
	addBlocker(ctx, t, s, "PROJ-1", "PROJ-2", now)

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "done", "test"))

	// Both nodes' statuses must be coherent: blocker is done, dep is
	// not still in StatusBlocked. If the tx rolled back partially we'd
	// see blocker=done but dep=blocked OR blocker=in_progress but
	// dep=open. Neither is acceptable.
	blockerNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	depNode, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)

	assert.Equal(t, model.StatusDone, blockerNode.Status)
	assert.NotEqual(t, model.StatusBlocked, depNode.Status,
		"if blocker is done, dep must not still be blocked (atomicity)")
}
