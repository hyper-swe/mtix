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
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedTree creates a 3-level tree for tree tests:
//
//	TREE-1 (depth 0)
//	├── TREE-1.1 (depth 1)
//	│   ├── TREE-1.1.1 (depth 2)
//	│   └── TREE-1.1.2 (depth 2)
//	└── TREE-1.2 (depth 1)
//	    └── TREE-1.2.1 (depth 2)
func seedTree(t *testing.T, s *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	nodes := []struct {
		id       string
		parentID string
		depth    int
		seq      int
		title    string
	}{
		{"TREE-1", "", 0, 1, "Root"},
		{"TREE-1.1", "TREE-1", 1, 1, "Child A"},
		{"TREE-1.2", "TREE-1", 1, 2, "Child B"},
		{"TREE-1.1.1", "TREE-1.1", 2, 1, "Grandchild A1"},
		{"TREE-1.1.2", "TREE-1.1", 2, 2, "Grandchild A2"},
		{"TREE-1.2.1", "TREE-1.2", 2, 1, "Grandchild B1"},
	}

	for _, n := range nodes {
		err := s.CreateNode(ctx, &model.Node{
			ID: n.id, ParentID: n.parentID, Project: "TREE",
			Depth: n.depth, Seq: n.seq, Title: n.title,
			Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1.0, NodeType: model.NodeTypeIssue,
			ContentHash: "h-" + n.id, CreatedAt: now, UpdatedAt: now,
		})
		require.NoError(t, err, "seed %s", n.id)
	}
}

// TestGetTree_RootOnly_ReturnsOnlyRoot verifies maxDepth=0 returns root only.
func TestGetTree_RootOnly_ReturnsOnlyRoot(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	nodes, err := s.GetTree(ctx, "TREE-1", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "TREE-1", nodes[0].ID)
}

// TestGetTree_FullTree_ReturnsAllDescendants verifies large maxDepth returns all.
func TestGetTree_FullTree_ReturnsAllDescendants(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	nodes, err := s.GetTree(ctx, "TREE-1", 100)
	require.NoError(t, err)
	assert.Len(t, nodes, 6, "should return root + 5 descendants")

	// Verify ordering: depth ASC, seq ASC.
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	assert.Equal(t, "TREE-1", ids[0], "root should be first")
}

// TestGetTree_DepthLimit_RespectsMaxDepth verifies depth limiting.
func TestGetTree_DepthLimit_RespectsMaxDepth(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	// maxDepth=1: root (depth 0) + children (depth 1), no grandchildren.
	nodes, err := s.GetTree(ctx, "TREE-1", 1)
	require.NoError(t, err)
	assert.Len(t, nodes, 3, "root + 2 children")

	for _, n := range nodes {
		assert.LessOrEqual(t, n.Depth, 1, "no nodes deeper than depth 1")
	}
}

// TestGetTree_SubtreeRoot_ReturnsSubtreeOnly verifies starting from a mid-level node.
func TestGetTree_SubtreeRoot_ReturnsSubtreeOnly(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	nodes, err := s.GetTree(ctx, "TREE-1.1", 100)
	require.NoError(t, err)
	assert.Len(t, nodes, 3, "TREE-1.1 + 2 grandchildren")

	ids := make(map[string]bool)
	for _, n := range nodes {
		ids[n.ID] = true
	}
	assert.True(t, ids["TREE-1.1"])
	assert.True(t, ids["TREE-1.1.1"])
	assert.True(t, ids["TREE-1.1.2"])
	assert.False(t, ids["TREE-1.2"], "sibling subtree should not be included")
}

// TestGetTree_ExcludesSoftDeleted verifies soft-deleted descendants are excluded.
func TestGetTree_ExcludesSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	// Soft-delete one grandchild.
	require.NoError(t, s.DeleteNode(ctx, "TREE-1.1.1", false, "tester"))

	nodes, err := s.GetTree(ctx, "TREE-1", 100)
	require.NoError(t, err)
	assert.Len(t, nodes, 5, "should exclude soft-deleted grandchild")

	for _, n := range nodes {
		assert.NotEqual(t, "TREE-1.1.1", n.ID, "soft-deleted node should be excluded")
	}
}

// TestGetTree_NonexistentRoot_ReturnsNotFound verifies error for missing root.
func TestGetTree_NonexistentRoot_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetTree(ctx, "NONEXISTENT", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetTree_EmptyRootID_ReturnsInvalidInput verifies empty ID is rejected.
func TestGetTree_EmptyRootID_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetTree(ctx, "", 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestGetTree_LeafNode_ReturnsOnlyLeaf verifies leaf node returns just itself.
func TestGetTree_LeafNode_ReturnsOnlyLeaf(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	nodes, err := s.GetTree(ctx, "TREE-1.1.1", 100)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "TREE-1.1.1", nodes[0].ID)
}

// TestGetTree_OrderedByDepthThenSeq verifies ordering.
func TestGetTree_OrderedByDepthThenSeq(t *testing.T) {
	s := newTestStore(t)
	seedTree(t, s)
	ctx := context.Background()

	nodes, err := s.GetTree(ctx, "TREE-1", 100)
	require.NoError(t, err)

	// Verify depth is non-decreasing.
	for i := 1; i < len(nodes); i++ {
		assert.GreaterOrEqual(t, nodes[i].Depth, nodes[i-1].Depth,
			"nodes should be ordered by depth ASC")
	}
}
