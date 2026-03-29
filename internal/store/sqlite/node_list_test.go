// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestListNodes_EmptyFilter_ReturnsAll verifies listing with no filters.
func TestListNodes_EmptyFilter_ReturnsAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "First", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Second", now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, nodes, 2)
}

// TestListNodes_StatusFilter_ReturnsMatching verifies status filtering.
func TestListNodes_StatusFilter_ReturnsMatching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Will finish", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Still open", now)))

	// Transition PROJ-1 to done.
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusInProgress, "starting", "test"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "done", "test"))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Status: []model.Status{model.StatusOpen},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PROJ-2", nodes[0].ID)
}

// TestListNodes_UnderFilter_ReturnsSubtree verifies subtree filtering.
func TestListNodes_UnderFilter_ReturnsSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child 1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Child 2", 1, 2, now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Unrelated", now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Under: "PROJ-1",
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 3, total) // Parent + 2 children.
	assert.Len(t, nodes, 3)
}

// TestListNodes_Pagination_RespectsLimitOffset verifies pagination.
func TestListNodes_Pagination_RespectsLimitOffset(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("PROJ-%d", i)
		require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PROJ", fmt.Sprintf("Node %d", i), now)))
	}

	// First page.
	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, nodes, 2)

	// Second page.
	nodes2, _, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 2, Offset: 2})
	require.NoError(t, err)
	assert.Len(t, nodes2, 2)
	assert.NotEqual(t, nodes[0].ID, nodes2[0].ID)
}

// TestListNodes_ZeroLimit_ReturnsCountOnly verifies count-only mode.
func TestListNodes_ZeroLimit_ReturnsCountOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Node 1", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Node 2", now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 0})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Nil(t, nodes)
}

// TestListNodes_ExcludesSoftDeleted verifies soft-deleted nodes are excluded.
func TestListNodes_ExcludesSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Active", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "To delete", now)))

	require.NoError(t, s.DeleteNode(ctx, "PROJ-2", false, "test"))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PROJ-1", nodes[0].ID)
}

// TestListNodes_AssigneeFilter_ReturnsMatching verifies assignee filtering.
func TestListNodes_AssigneeFilter_ReturnsMatching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Assigned", now)))
	require.NoError(t, s.ClaimNode(ctx, "PROJ-1", "agent-001"))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "Unassigned", now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Assignee: "agent-001",
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PROJ-1", nodes[0].ID)
}

// TestListNodes_PriorityFilter_ReturnsMatching verifies priority filtering.
func TestListNodes_PriorityFilter_ReturnsMatching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("PROJ-1", "PROJ", "Critical", now)
	n1.Priority = model.PriorityCritical
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("PROJ-2", "PROJ", "Low", now)
	n2.Priority = model.PriorityLow
	require.NoError(t, s.CreateNode(ctx, n2))

	p := int(model.PriorityCritical)
	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Priority: &p,
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PROJ-1", nodes[0].ID)
}

// TestListNodes_NodeTypeFilter_ReturnsMatching verifies node_type filtering.
func TestListNodes_NodeTypeFilter_ReturnsMatching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("PROJ-1", "PROJ", "Story", now)
	n1.NodeType = model.NodeTypeStory
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("PROJ-2", "PROJ", "Issue", now)
	n2.NodeType = model.NodeTypeIssue
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		NodeType: string(model.NodeTypeIssue),
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PROJ-2", nodes[0].ID)
}

// TestListNodes_LabelsFilter_ReturnsMatching verifies label-based filtering.
func TestListNodes_LabelsFilter_ReturnsMatching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("PROJ-1", "PROJ", "Labeled Node", now)
	n1.Labels = []string{"bug", "urgent"}
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("PROJ-2", "PROJ", "Other Node", now)
	n2.Labels = []string{"feature"}
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Labels: []string{"bug"},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PROJ-1", nodes[0].ID)
}

// TestListNodes_MultipleStatusFilter_ReturnsMatching verifies multiple status filtering.
func TestListNodes_MultipleStatusFilter_ReturnsMatching(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Open", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-2", "PROJ", "In progress", now)))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2", model.StatusInProgress, "wip", "test"))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-3", "PROJ", "Done", now)))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-3", model.StatusInProgress, "wip", "test"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-3", model.StatusDone, "done", "test"))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Status: []model.Status{model.StatusOpen, model.StatusInProgress},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, nodes, 2)
}
