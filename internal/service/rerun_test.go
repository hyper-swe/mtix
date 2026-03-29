// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestRerun_AllStrategy_InvalidatesAndResetsDescendants verifies --all strategy.
func TestRerun_AllStrategy_InvalidatesAndResetsDescendants(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	// Create children with various statuses.
	child1, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Open Child", Creator: "admin",
	})
	require.NoError(t, err)

	child2, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Done Child", Creator: "admin",
	})
	require.NoError(t, err)
	// Move child2 to done.
	require.NoError(t, s.ClaimNode(ctx, child2.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, child2.ID, model.StatusDone, "done", "agent-1"))

	// Rerun with --all strategy.
	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "prompt changed", "admin")
	require.NoError(t, err)

	// Both children should be open (invalidated then reset).
	got1, err := s.GetNode(ctx, child1.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got1.Status)

	got2, err := s.GetNode(ctx, child2.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got2.Status)
}

// TestRerun_OpenOnlyStrategy_SkipsDoneDescendants verifies --open-only strategy.
func TestRerun_OpenOnlyStrategy_SkipsDoneDescendants(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	openChild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Open Child", Creator: "admin",
	})
	require.NoError(t, err)

	doneChild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Done Child", Creator: "admin",
	})
	require.NoError(t, err)
	require.NoError(t, s.ClaimNode(ctx, doneChild.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, doneChild.ID, model.StatusDone, "done", "agent-1"))

	err = svc.Rerun(ctx, parent.ID, service.RerunOpenOnly, "prompt changed", "admin")
	require.NoError(t, err)

	// Open child should be reset to open.
	got, err := s.GetNode(ctx, openChild.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)

	// Done child should remain done.
	gotDone, err := s.GetNode(ctx, doneChild.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, gotDone.Status)
}

// TestRerun_DeleteStrategy_InvalidatesBeforeSoftDelete verifies FR-3.5b.
func TestRerun_DeleteStrategy_InvalidatesBeforeSoftDelete(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "To Delete", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, parent.ID, service.RerunDelete, "prompt changed", "admin")
	require.NoError(t, err)

	// Child should be soft-deleted (not found).
	_, err = s.GetNode(ctx, child.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)

	// FR-3.5b: When undeleted, should be invalidated (not open).
	require.NoError(t, s.UndeleteNode(ctx, child.ID))
	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, got.Status)
}

// TestRerun_ReviewStrategy_SetsInvalidatedForReview verifies --review strategy.
func TestRerun_ReviewStrategy_SetsInvalidatedForReview(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "To Review", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, parent.ID, service.RerunReview, "needs review", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, got.Status)
}

// TestRerun_DoneParent_AutoReopensFirst_FR3_5c verifies auto-reopen for done parent.
func TestRerun_DoneParent_AutoReopensFirst_FR3_5c(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Done Parent", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Set parent to done.
	require.NoError(t, s.ClaimNode(ctx, parent.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, parent.ID, model.StatusDone, "done", "agent-1"))

	// Rerun should auto-reopen the parent first.
	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "prompt changed", "admin")
	require.NoError(t, err)

	// Parent should now be open (auto-reopened by rerun).
	got, err := s.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	// closed_at should be cleared.
	assert.Nil(t, got.ClosedAt)
}

// TestRerun_CancelledParent_AutoReopensFirst verifies auto-reopen for cancelled parent.
func TestRerun_CancelledParent_AutoReopensFirst(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Cancelled Parent", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	require.NoError(t, s.CancelNode(ctx, parent.ID, "descoping", "admin", false))

	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "revised", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRerun_InvalidatedParent_RestoresThenReopens verifies FR-3.5c invalidated parent path.
func TestRerun_InvalidatedParent_RestoresThenReopens(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Set parent to done, then invalidate it.
	require.NoError(t, s.ClaimNode(ctx, parent.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, parent.ID, model.StatusDone, "done", "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, parent.ID, model.StatusInvalidated, "stale", "system"))

	// Rerun should restore (invalidated → done via previous_status), then auto-reopen (done → open).
	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "re-revised", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRestore_InvalidatedNode_ReturnsToPreviousStatus verifies restore.
func TestRestore_InvalidatedNode_ReturnsToPreviousStatus(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "To Invalidate", Creator: "admin",
	})
	require.NoError(t, err)

	// Move to in_progress, then invalidate.
	require.NoError(t, s.ClaimNode(ctx, node.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, node.ID, model.StatusInvalidated, "stale", "system"))

	// Restore should return to in_progress (previous_status).
	err = svc.Restore(ctx, node.ID, "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestRestore_NonInvalidatedNode_ReturnsInvalidTransition verifies rejection.
func TestRestore_NonInvalidatedNode_ReturnsInvalidTransition(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Open Node", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Restore(ctx, node.ID, "admin")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestRerun_NoChildren_ReturnsNil verifies no-op when no children exist.
func TestRerun_NoChildren_ReturnsNil(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Leaf Node", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "no children", "admin")
	assert.NoError(t, err)
}

// TestRerun_NestedChildren_ProcessesDepthFirst verifies recursive descendant processing.
func TestRerun_NestedChildren_ProcessesDepthFirst(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	grandchild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: child.ID, Project: "PROJ", Title: "Grandchild", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, root.ID, service.RerunAll, "deep rerun", "admin")
	require.NoError(t, err)

	// Both child and grandchild should be reset to open.
	gotChild, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, gotChild.Status)

	gotGC, err := s.GetNode(ctx, grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, gotGC.Status)
}

// TestRerun_DeleteStrategy_NestedChildren verifies recursive delete strategy.
func TestRerun_DeleteStrategy_NestedChildren(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	grandchild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: child.ID, Project: "PROJ", Title: "Grandchild", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, root.ID, service.RerunDelete, "delete all", "admin")
	require.NoError(t, err)

	// Both should be soft-deleted.
	_, err = s.GetNode(ctx, child.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = s.GetNode(ctx, grandchild.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestRerun_OpenOnlyStrategy_NestedChildren verifies open-only on nested tree.
func TestRerun_OpenOnlyStrategy_NestedChildren(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Create grandchild while child is still open.
	grandchild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: child.ID, Project: "PROJ", Title: "Grandchild", Creator: "admin",
	})
	require.NoError(t, err)

	// Now move child to done.
	require.NoError(t, s.ClaimNode(ctx, child.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, child.ID, model.StatusDone, "done", "agent-1"))

	err = svc.Rerun(ctx, root.ID, service.RerunOpenOnly, "re-open", "admin")
	require.NoError(t, err)

	// Done child stays done.
	gotChild, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, gotChild.Status)

	// Open grandchild is reset.
	gotGC, err := s.GetNode(ctx, grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, gotGC.Status)
}

// TestRerun_UnknownStrategy_ReturnsError verifies unknown strategy rejection.
func TestRerun_UnknownStrategy_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, parent.ID, service.RerunStrategy("invalid_strategy"), "test", "admin")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestRestore_InvalidatedNode_NoPreviousStatus_DefaultsToOpen verifies fallback.
func TestRestore_InvalidatedNode_NoPreviousStatus_DefaultsToOpen(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "To Invalidate", Creator: "admin",
	})
	require.NoError(t, err)

	// Invalidate directly from open (previous_status should be "open").
	require.NoError(t, s.TransitionStatus(ctx, node.ID, model.StatusInvalidated, "stale", "system"))

	// Restore should go back to open.
	err = svc.Restore(ctx, node.ID, "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRestore_NonExistentNode_ReturnsError verifies error handling.
func TestRestore_NonExistentNode_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	err := svc.Restore(ctx, "NONEXISTENT", "admin")
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestRerun_ReviewStrategy_NestedChildren verifies recursive review strategy.
func TestRerun_ReviewStrategy_NestedChildren(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	grandchild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: child.ID, Project: "PROJ", Title: "Grandchild", Creator: "admin",
	})
	require.NoError(t, err)

	err = svc.Rerun(ctx, root.ID, service.RerunReview, "review all", "admin")
	require.NoError(t, err)

	gotChild, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, gotChild.Status)

	gotGC, err := s.GetNode(ctx, grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, gotGC.Status)
}

// TestRerun_NonExistentNode_ReturnsError verifies error for unknown node.
func TestRerun_NonExistentNode_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	err := svc.Rerun(ctx, "NONEXISTENT", service.RerunAll, "test", "admin")
	assert.Error(t, err)
}

// TestRerun_BroadcastsBatchEvent verifies batch event broadcasting.
func TestRerun_BroadcastsBatchEvent(t *testing.T) {
	svc, _, bc := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()
	err = svc.Rerun(ctx, parent.ID, service.RerunReview, "review", "admin")
	require.NoError(t, err)

	events := bc.Events()
	var hasInvalidated, hasProgress bool
	for _, e := range events {
		if e.Type == service.EventNodesInvalidated {
			hasInvalidated = true
		}
		if e.Type == service.EventProgressChanged {
			hasProgress = true
		}
	}
	assert.True(t, hasInvalidated, "should broadcast nodes.invalidated")
	assert.True(t, hasProgress, "should broadcast progress.changed")
}
