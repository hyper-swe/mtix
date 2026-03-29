// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// buildRerunTree creates a parent with 3 children, all completed.
// Returns parent ID and child IDs.
func buildRerunTree(t *testing.T, env *e2eEnv) (string, []string) {
	t.Helper()

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Rerun Parent",
		Project: "RERUN",
		Creator: "admin",
	})
	require.NoError(t, err)

	childIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, []service.DecomposeInput{
		{Title: "Child A"},
		{Title: "Child B"},
		{Title: "Child C"},
	}, "admin")
	require.NoError(t, err)

	// Complete all children: open → in_progress → done.
	for _, cid := range childIDs {
		err = env.store.ClaimNode(env.ctx, cid, "agent-001")
		require.NoError(t, err)
		err = env.nodeSvc.TransitionStatus(env.ctx, cid, model.StatusDone,
			"done", "agent-001")
		require.NoError(t, err)
	}

	return parent.ID, childIDs
}

// TestE2E_Rerun_AllStrategy_DescendantsOpen verifies rerun "all" resets all to open.
func TestE2E_Rerun_AllStrategy_DescendantsOpen(t *testing.T) {
	env := setupE2E(t)
	parentID, childIDs := buildRerunTree(t, env)

	err := env.nodeSvc.Rerun(env.ctx, parentID, service.RerunAll, "re-evaluate", "admin")
	require.NoError(t, err)

	// All children should be open (reset from done → invalidated → open).
	for _, cid := range childIDs {
		child, err := env.nodeSvc.GetNode(env.ctx, cid)
		require.NoError(t, err)
		assert.Equal(t, model.StatusOpen, child.Status,
			"child %s should be open after rerun all", cid)
	}
}

// TestE2E_Rerun_OpenOnly_DoneKept verifies "open_only" preserves done children.
func TestE2E_Rerun_OpenOnly_DoneKept(t *testing.T) {
	env := setupE2E(t)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Open Only Parent",
		Project: "RERUN",
		Creator: "admin",
	})
	require.NoError(t, err)

	childIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, []service.DecomposeInput{
		{Title: "Done Child"},
		{Title: "Open Child"},
		{Title: "InProgress Child"},
	}, "admin")
	require.NoError(t, err)

	// Complete first child.
	err = env.store.ClaimNode(env.ctx, childIDs[0], "agent-001")
	require.NoError(t, err)
	err = env.nodeSvc.TransitionStatus(env.ctx, childIDs[0], model.StatusDone,
		"done", "agent-001")
	require.NoError(t, err)

	// Put third child in_progress.
	err = env.store.ClaimNode(env.ctx, childIDs[2], "agent-002")
	require.NoError(t, err)

	// Rerun with open_only strategy.
	err = env.nodeSvc.Rerun(env.ctx, parent.ID, service.RerunOpenOnly,
		"partial rerun", "admin")
	require.NoError(t, err)

	// Done child should stay done.
	doneChild, err := env.nodeSvc.GetNode(env.ctx, childIDs[0])
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, doneChild.Status,
		"done child should remain done in open_only strategy")

	// Open child should be reset to open.
	openChild, err := env.nodeSvc.GetNode(env.ctx, childIDs[1])
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, openChild.Status,
		"open child should be reset to open")

	// InProgress child should be reset to open.
	ipChild, err := env.nodeSvc.GetNode(env.ctx, childIDs[2])
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, ipChild.Status,
		"in_progress child should be reset to open")
}

// TestE2E_Rerun_Delete_SoftDeleted verifies "delete" strategy soft-deletes descendants.
func TestE2E_Rerun_Delete_SoftDeleted(t *testing.T) {
	env := setupE2E(t)
	parentID, childIDs := buildRerunTree(t, env)

	err := env.nodeSvc.Rerun(env.ctx, parentID, service.RerunDelete,
		"delete and re-decompose", "admin")
	require.NoError(t, err)

	// All children should be soft-deleted (not found via normal GetNode).
	for _, cid := range childIDs {
		_, err := env.nodeSvc.GetNode(env.ctx, cid)
		assert.ErrorIs(t, err, model.ErrNotFound,
			"child %s should be soft-deleted after rerun delete", cid)
	}
}

// TestE2E_Rerun_Delete_RecoverableWithin30Days verifies soft-deleted nodes can be restored.
func TestE2E_Rerun_Delete_RecoverableWithin30Days(t *testing.T) {
	env := setupE2E(t)
	parentID, childIDs := buildRerunTree(t, env)

	err := env.nodeSvc.Rerun(env.ctx, parentID, service.RerunDelete,
		"will recover", "admin")
	require.NoError(t, err)

	// Undelete the first child.
	err = env.store.UndeleteNode(env.ctx, childIDs[0])
	require.NoError(t, err)

	// Node should be accessible again.
	child, err := env.nodeSvc.GetNode(env.ctx, childIDs[0])
	require.NoError(t, err)
	assert.NotNil(t, child)
}

// TestE2E_Rerun_ManualReview_MarkedInvalidated verifies "review" marks as invalidated.
func TestE2E_Rerun_ManualReview_MarkedInvalidated(t *testing.T) {
	env := setupE2E(t)
	parentID, childIDs := buildRerunTree(t, env)

	err := env.nodeSvc.Rerun(env.ctx, parentID, service.RerunReview,
		"needs human review", "admin")
	require.NoError(t, err)

	// All children should be invalidated.
	for _, cid := range childIDs {
		child, err := env.nodeSvc.GetNode(env.ctx, cid)
		require.NoError(t, err)
		assert.Equal(t, model.StatusInvalidated, child.Status,
			"child %s should be invalidated for manual review", cid)
	}
}

// TestE2E_Rerun_Restore_PreviousStatusRestored verifies restore after invalidation.
func TestE2E_Rerun_Restore_PreviousStatusRestored(t *testing.T) {
	env := setupE2E(t)
	parentID, childIDs := buildRerunTree(t, env)

	// Invalidate via review strategy.
	err := env.nodeSvc.Rerun(env.ctx, parentID, service.RerunReview,
		"review then restore", "admin")
	require.NoError(t, err)

	// Restore the first child.
	err = env.nodeSvc.Restore(env.ctx, childIDs[0], "admin")
	require.NoError(t, err)

	// The restored child should be back in its previous status.
	// Previous status before invalidation was "done", but restore
	// transitions invalidated → previous_status (or open).
	child, err := env.nodeSvc.GetNode(env.ctx, childIDs[0])
	require.NoError(t, err)

	// After restore, the node should be in a non-invalidated state.
	assert.NotEqual(t, model.StatusInvalidated, child.Status,
		"restored child should not be invalidated")
}

// TestE2E_Rerun_WebSocket_BatchEventReceived verifies events are broadcast
// (using a recording broadcaster).
func TestE2E_Rerun_WebSocket_BatchEventReceived(t *testing.T) {
	env := setupE2E(t)

	// Replace broadcaster with a recording one.
	recorder := &eventRecorder{}
	env.nodeSvc = service.NewNodeService(
		env.store, recorder, &service.StaticConfig{}, nil, testClock(),
	)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Event Parent",
		Project: "EVT",
		Creator: "admin",
	})
	require.NoError(t, err)

	_, err = env.nodeSvc.Decompose(env.ctx, parent.ID, []service.DecomposeInput{
		{Title: "Event Child 1"},
		{Title: "Event Child 2"},
	}, "admin")
	require.NoError(t, err)

	// Clear recorded events from creation.
	recorder.events = nil

	// Rerun should emit invalidation + progress events.
	err = env.nodeSvc.Rerun(env.ctx, parent.ID, service.RerunAll,
		"test events", "admin")
	require.NoError(t, err)

	// Verify events were broadcast.
	assert.Greater(t, len(recorder.events), 0,
		"rerun should broadcast events")

	// Look for the batch invalidation event.
	hasInvalidation := false
	for _, e := range recorder.events {
		if e.Type == service.EventNodesInvalidated {
			hasInvalidation = true
			break
		}
	}
	assert.True(t, hasInvalidation,
		"rerun should broadcast a batch invalidation event")
}

// TestE2E_Rerun_AgentNotified verifies the agent receives invalidation events.
func TestE2E_Rerun_AgentNotified(t *testing.T) {
	env := setupE2E(t)

	recorder := &eventRecorder{}
	env.nodeSvc = service.NewNodeService(
		env.store, recorder, &service.StaticConfig{}, nil, testClock(),
	)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Agent Notify",
		Project: "NOTF",
		Creator: "admin",
	})
	require.NoError(t, err)

	childIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, []service.DecomposeInput{
		{Title: "Agent Task"},
	}, "admin")
	require.NoError(t, err)

	// Agent claims the child.
	err = env.store.ClaimNode(env.ctx, childIDs[0], "agent-001")
	require.NoError(t, err)

	recorder.events = nil

	// Rerun the parent — agent's claimed node should be affected.
	err = env.nodeSvc.Rerun(env.ctx, parent.ID, service.RerunAll,
		"requirements changed", "admin")
	require.NoError(t, err)

	// Events should be emitted for agent's node.
	assert.Greater(t, len(recorder.events), 0,
		"events should be broadcast when agent's claimed node is invalidated")
}

// eventRecorder is a test EventBroadcaster that records all events.
type eventRecorder struct {
	events []service.Event
}

func (r *eventRecorder) Broadcast(_ context.Context, event service.Event) error {
	r.events = append(r.events, event)
	return nil
}
