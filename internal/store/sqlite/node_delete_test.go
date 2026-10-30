// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Parallel cases retain owned fixtures and existing behavior assertions.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestDeleteNode_SetsDeletedAt verifies soft-delete sets deleted_at.
func TestDeleteNode_SetsDeletedAt(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.DeleteNode(ctx, "PROJ-1", false, "admin-user")
	require.NoError(t, err)

	// Node should not be findable via GetNode (excludes deleted).
	_, err = s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Verify deleted_at is set by querying raw.
	var deletedAt, deletedBy string
	err = s.QueryRow(ctx,
		"SELECT deleted_at, deleted_by FROM nodes WHERE id = ?", "PROJ-1",
	).Scan(&deletedAt, &deletedBy)
	require.NoError(t, err)
	assert.NotEmpty(t, deletedAt)
	assert.Equal(t, "admin-user", deletedBy)
}

// TestDeleteNode_CascadeDefault_DeletesDescendants verifies cascade delete.
func TestDeleteNode_CascadeDefault_DeletesDescendants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a tree: PROJ-1 → PROJ-1.1 → PROJ-1.1.1
	root := makeRootNode("PROJ-1", "PROJ", "Root", now)
	require.NoError(t, s.CreateNode(ctx, root))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	grandchild := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Grandchild", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, grandchild))

	// Delete root with cascade=true.
	err := s.DeleteNode(ctx, "PROJ-1", true, "admin")
	require.NoError(t, err)

	// All nodes should be soft-deleted.
	_, err = s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound, "root should be deleted")

	_, err = s.GetNode(ctx, "PROJ-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound, "child should be deleted")

	_, err = s.GetNode(ctx, "PROJ-1.1.1")
	assert.ErrorIs(t, err, model.ErrNotFound, "grandchild should be deleted")
}

// TestDeleteNode_NoCascade_ChildrenBecomeOrphans verifies non-cascade delete.
func TestDeleteNode_NoCascade_ChildrenBecomeOrphans(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	// Delete parent with cascade=false.
	err := s.DeleteNode(ctx, "PROJ-1", false, "admin")
	require.NoError(t, err)

	// Parent should be deleted.
	_, err = s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Child should still exist (orphaned).
	got, err := s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1.1", got.ID)
}

// TestDeleteNode_RecalculatesParentProgress verifies parent progress update.
func TestDeleteNode_RecalculatesParentProgress(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create two children: one done (1.0), one open (0.0).
	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done Child", 1, 1, now)
	child1.Progress = 1.0
	child1.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Open Child", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	// Parent should be at 0.5.
	parentNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, parentNode.Progress)

	// Delete the open child.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.2", false, "admin"))

	// Parent should now be at 1.0 (only done child remains).
	parentNode, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, parentNode.Progress)
}

// TestDeleteNode_CreatesActivityEntry is a placeholder — activity recording
// is handled in the service layer for delete operations.
func TestDeleteNode_CreatesActivityEntry(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.DeleteNode(ctx, "PROJ-1", false, "admin")
	require.NoError(t, err)

	// Verify deletion occurred (basic verification).
	_, err = s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDeleteNode_AlreadyDeleted_ReturnsNotFound verifies double-delete protection.
func TestDeleteNode_AlreadyDeleted_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// First delete should succeed.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// Second delete should return ErrNotFound.
	err := s.DeleteNode(ctx, "PROJ-1", false, "admin")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUndeleteNode_RestoresNode verifies undelete restores the node.
func TestUndeleteNode_RestoresNode(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Restore", now)
	require.NoError(t, s.CreateNode(ctx, node))

	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// Verify it's deleted.
	_, err := s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Undelete.
	err = s.UndeleteNode(ctx, "PROJ-1")
	require.NoError(t, err)

	// Should be accessible again.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", got.ID)
	assert.Nil(t, got.DeletedAt)
}

// TestUndeleteNode_RestoresDescendants verifies undelete cascades to descendants.
func TestUndeleteNode_RestoresDescendants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeRootNode("PROJ-1", "PROJ", "Root", now)
	require.NoError(t, s.CreateNode(ctx, root))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	grandchild := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Grandchild", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, grandchild))

	// Cascade delete from root.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", true, "admin"))

	// Undelete root.
	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1"))

	// All should be restored.
	_, err := s.GetNode(ctx, "PROJ-1")
	assert.NoError(t, err, "root should be restored")

	_, err = s.GetNode(ctx, "PROJ-1.1")
	assert.NoError(t, err, "child should be restored")

	_, err = s.GetNode(ctx, "PROJ-1.1.1")
	assert.NoError(t, err, "grandchild should be restored")
}

// TestUndeleteNode_RecalculatesParentProgress verifies progress recalculation.
func TestUndeleteNode_RecalculatesParentProgress(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done Child", 1, 1, now)
	child1.Progress = 1.0
	child1.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Open Child", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	// Delete child2.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.2", false, "admin"))

	// Parent should be at 1.0.
	parentNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, parentNode.Progress)

	// Undelete child2.
	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1.2"))

	// Parent should be back to 0.5.
	parentNode, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, parentNode.Progress)
}

// TestUndeleteNode_BeyondRetention_ReturnsNotFound verifies retention enforcement.
// In this implementation, retention is not yet enforced at the store level.
// A node that doesn't exist as deleted returns ErrNotFound.
func TestUndeleteNode_BeyondRetention_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	// Try to undelete a node that was never created.
	err := s.UndeleteNode(ctx, "NONEXISTENT-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDeleteNode_NonExistent_ReturnsNotFound verifies missing node deletion.
func TestDeleteNode_NonExistent_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	err := s.DeleteNode(ctx, "NONEXISTENT-1", false, "admin")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUndeleteNode_NotDeleted_ReturnsNotFound verifies undelete of active node.
func TestUndeleteNode_NotDeleted_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Active Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Trying to undelete an active (non-deleted) node should return ErrNotFound.
	err := s.UndeleteNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDeleteNode_InvalidateBeforeDelete_WhenRerunDelete verifies FR-3.5b behavior.
// When a node is re-run with --delete, it should be invalidated before deletion.
// This is a service-layer concern; at the store layer, we verify the basic delete.
func TestDeleteNode_InvalidateBeforeDelete_WhenRerunDelete(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Rerun Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// At the store level, a simple delete succeeds.
	err := s.DeleteNode(ctx, "PROJ-1", false, "admin")
	require.NoError(t, err)

	_, err = s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDeleteNodeWithRoot_RejectsInvalidGroups verifies boundary validation
// before a grouped delete can mutate a node (MTIX-107.20).
func TestDeleteNodeWithRoot_RejectsInvalidGroups(t *testing.T) {
	s := newTestStore(t)
	deleter, ok := any(s).(interface {
		DeleteNodeWithRoot(context.Context, string, string, string) error
	})
	require.True(t, ok, "store supports explicit recovery groups")
	for _, rootID := range []string{"", "TEST-2", "TEST-1.10", "TEST-1.1.1"} {
		t.Run(rootID, func(t *testing.T) {
			err := deleter.DeleteNodeWithRoot(context.Background(), "TEST-1.1", rootID, "admin")
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
	err := deleter.DeleteNodeWithRoot(context.Background(), "TEST-1.1", "TEST-1", "admin")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDeleteNodeWithRoot_SiblingSharingARootPrefix_IsRejected pins the
// dot boundary of the root check: TEST-1.10 starts with the text TEST-1.1 but
// is a sibling of TEST-1.1, not a descendant, so TEST-1.1 cannot be its group.
func TestDeleteNodeWithRoot_SiblingSharingARootPrefix_IsRejected(t *testing.T) {
	s := newTestStore(t)
	deleter, ok := any(s).(interface {
		DeleteNodeWithRoot(context.Context, string, string, string) error
	})
	require.True(t, ok, "store supports explicit recovery groups")
	err := deleter.DeleteNodeWithRoot(context.Background(), "TEST-1.10", "TEST-1.1", "admin")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	err = deleter.DeleteNodeWithRoot(context.Background(), "TEST-1.1.1", "TEST-1.1", "admin")
	assert.ErrorIs(t, err, model.ErrNotFound, "a real descendant passes validation")
}

// TestDeleteNodeWithRoot_AncestorRoot_DeletesOnlyTheNode is the positive store
// path without grouping effects: one call deletes exactly one node and never
// cascades to its children or parent.
func TestDeleteNodeWithRoot_AncestorRoot_DeletesOnlyTheNode(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Grandchild", 2, 1, now)))
	deleter, ok := any(s).(interface {
		DeleteNodeWithRoot(context.Context, string, string, string) error
	})
	require.True(t, ok, "store supports explicit recovery groups")

	require.NoError(t, deleter.DeleteNodeWithRoot(ctx, "PROJ-1.1", "PROJ-1", "admin"))
	_, err := s.GetNode(ctx, "PROJ-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound, "the node itself is deleted")
	for _, id := range []string{"PROJ-1", "PROJ-1.1.1"} {
		_, getErr := s.GetNode(ctx, id)
		assert.NoError(t, getErr, "%s is not deleted: the call does not cascade", id)
	}
}

// TestDeleteNodeWithRoot_AncestorRoot_RecalculatesParentProgress pins FR-5.7
// for a grouped delete: deleting a child under an ancestor recovery root
// (the root differs from the deleted id) still recomputes the surviving
// parent's progress in the same transaction (MTIX-107.20.1).
func TestDeleteNodeWithRoot_AncestorRoot_RecalculatesParentProgress(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Parent", now)))
	done := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done Child", 1, 1, now)
	done.Progress = 1.0
	done.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, done))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Open Child", 1, 2, now)))
	before, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, 0.5, before.Progress, "one done and one open child")

	require.NoError(t, s.DeleteNodeWithRoot(ctx, "PROJ-1.2", "PROJ-1", "admin"))

	after, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, after.Progress, "only the done child remains, so the parent rolls up to 1.0")
}

// TestDeleteNodeWithRoot_GrandparentRoot_RecalculatesDirectParentAndGrandparent
// pins FR-5.7 when the recovery root is two levels above the deleted node: the
// rollup starts at the direct parent (not at the root) and reaches the
// grandparent too (MTIX-107.20.1).
func TestDeleteNodeWithRoot_GrandparentRoot_RecalculatesDirectParentAndGrandparent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Grandparent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Parent", 1, 1, now)))
	done := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Done Grandchild", 2, 1, now)
	done.Progress = 1.0
	done.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, done))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1.2", "PROJ-1.1", "PROJ", "Open Grandchild", 2, 2, now)))
	for _, id := range []string{"PROJ-1.1", "PROJ-1"} {
		before, err := s.GetNode(ctx, id)
		require.NoError(t, err)
		require.Equal(t, 0.5, before.Progress, "%s starts at one done and one open leaf", id)
	}

	require.NoError(t, s.DeleteNodeWithRoot(ctx, "PROJ-1.1.2", "PROJ-1", "admin"))

	for _, id := range []string{"PROJ-1.1", "PROJ-1"} {
		after, err := s.GetNode(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, 1.0, after.Progress, "%s rolls up to 1.0 once the open leaf is deleted", id)
	}
}

// TestDeleteNodeWithRoot_SharedRoot_OneUndeleteRestoresTheGroup pins the
// grouping: nodes deleted one by one under the same root come back together.
func TestDeleteNodeWithRoot_SharedRoot_OneUndeleteRestoresTheGroup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Grandchild", 2, 1, now)))
	deleter, ok := any(s).(interface {
		DeleteNodeWithRoot(context.Context, string, string, string) error
	})
	require.True(t, ok, "store supports explicit recovery groups")

	require.NoError(t, deleter.DeleteNodeWithRoot(ctx, "PROJ-1.1.1", "PROJ-1.1", "admin"))
	require.NoError(t, deleter.DeleteNodeWithRoot(ctx, "PROJ-1.1", "PROJ-1.1", "admin"))
	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1.1"))
	for _, id := range []string{"PROJ-1.1", "PROJ-1.1.1"} {
		_, err := s.GetNode(ctx, id)
		assert.NoError(t, err, "%s is restored by one undelete of the group root", id)
	}
}
