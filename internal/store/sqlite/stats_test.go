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

// seedStatsNodes creates nodes with varied statuses, priorities, and types.
func seedStatsNodes(t *testing.T, s *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	nodes := []struct {
		id       string
		parentID string
		depth    int
		seq      int
		status   model.Status
		priority model.Priority
		nodeType model.NodeType
		progress float64
		weight   float64
	}{
		{"STAT-1", "", 0, 1, model.StatusOpen, model.PriorityHigh, model.NodeTypeStory, 0.0, 2.0},
		{"STAT-1.1", "STAT-1", 1, 1, model.StatusOpen, model.PriorityMedium, model.NodeTypeEpic, 0.0, 1.0},
		{"STAT-1.2", "STAT-1", 1, 2, model.StatusDone, model.PriorityLow, model.NodeTypeIssue, 1.0, 1.0},
		{"STAT-2", "", 0, 2, model.StatusOpen, model.PriorityHigh, model.NodeTypeMicro, 0.5, 1.0},
		{"STAT-3", "", 0, 3, model.StatusBlocked, model.PriorityCritical, model.NodeTypeIssue, 0.0, 1.0},
	}

	for _, n := range nodes {
		err := s.CreateNode(ctx, &model.Node{
			ID: n.id, ParentID: n.parentID, Project: "STAT",
			Depth: n.depth, Seq: n.seq, Title: "Stats node " + n.id,
			Status: n.status, Priority: n.priority, NodeType: n.nodeType,
			Progress: n.progress, Weight: n.weight,
			ContentHash: "h-" + n.id, CreatedAt: now, UpdatedAt: now,
		})
		require.NoError(t, err, "seed %s", n.id)
	}
}

// TestGetStats_GlobalScope_ReturnsAllCounts verifies global statistics.
func TestGetStats_GlobalScope_ReturnsAllCounts(t *testing.T) {
	s := newTestStore(t)
	seedStatsNodes(t, s)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	require.NotNil(t, stats)

	assert.Equal(t, 5, stats.TotalNodes)
	assert.Empty(t, stats.ScopeID, "global scope should have empty scope ID")

	// Status breakdown: 3 open, 1 done, 1 blocked.
	assert.Equal(t, 3, stats.ByStatus["open"])
	assert.Equal(t, 1, stats.ByStatus["done"])
	assert.Equal(t, 1, stats.ByStatus["blocked"])

	// Priority breakdown.
	assert.Equal(t, 2, stats.ByPriority["2"], "2 high priority nodes")
	assert.Equal(t, 1, stats.ByPriority["3"], "1 medium priority node")
	assert.Equal(t, 1, stats.ByPriority["4"], "1 low priority node")
	assert.Equal(t, 1, stats.ByPriority["1"], "1 critical priority node")

	// Type breakdown.
	assert.Equal(t, 2, stats.ByType["issue"])
	assert.Equal(t, 1, stats.ByType["story"])
	assert.Equal(t, 1, stats.ByType["epic"])
	assert.Equal(t, 1, stats.ByType["micro"])
}

// TestGetStats_ScopedToSubtree_ReturnsSubtreeCounts verifies scoped statistics.
func TestGetStats_ScopedToSubtree_ReturnsSubtreeCounts(t *testing.T) {
	s := newTestStore(t)
	seedStatsNodes(t, s)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "STAT-1")
	require.NoError(t, err)
	require.NotNil(t, stats)

	assert.Equal(t, "STAT-1", stats.ScopeID)
	assert.Equal(t, 3, stats.TotalNodes, "STAT-1 + 2 children")

	// Status: 2 open, 1 done within STAT-1 subtree.
	assert.Equal(t, 2, stats.ByStatus["open"])
	assert.Equal(t, 1, stats.ByStatus["done"])
}

// TestGetStats_EmptyDB_ReturnsZeroCounts verifies empty database handling.
func TestGetStats_EmptyDB_ReturnsZeroCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	require.NotNil(t, stats)

	assert.Equal(t, 0, stats.TotalNodes)
	assert.Empty(t, stats.ByStatus)
	assert.Empty(t, stats.ByPriority)
	assert.Empty(t, stats.ByType)
	assert.Equal(t, 0.0, stats.Progress)
}

// TestGetStats_ExcludesSoftDeleted verifies soft-deleted nodes are excluded.
func TestGetStats_ExcludesSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	seedStatsNodes(t, s)
	ctx := context.Background()

	// Soft-delete one node.
	require.NoError(t, s.DeleteNode(ctx, "STAT-3", false, "tester"))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 4, stats.TotalNodes, "should exclude soft-deleted node")
	assert.Equal(t, 0, stats.ByStatus["blocked"], "blocked node was deleted")
}

// TestGetStats_Progress_WeightedAverage verifies weighted progress calculation.
func TestGetStats_Progress_WeightedAverage(t *testing.T) {
	s := newTestStore(t)
	seedStatsNodes(t, s)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)

	// Expected: sum(progress*weight) / sum(weight)
	// STAT-1 progress was recalculated to 0.5 by child creation triggers
	// (STAT-1.1 open=0.0 + STAT-1.2 done=1.0 → 0.5). So:
	// (0.5*2 + 0*1 + 1.0*1 + 0.5*1 + 0*1) / (2+1+1+1+1) = 2.5/6 ≈ 0.4167
	assert.InDelta(t, 2.5/6.0, stats.Progress, 0.001, "weighted average progress")
}

// TestGetStats_NonexistentScope_ReturnsZeroCounts verifies missing scope returns empty.
func TestGetStats_NonexistentScope_ReturnsZeroCounts(t *testing.T) {
	s := newTestStore(t)
	seedStatsNodes(t, s)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "NONEXISTENT")
	require.NoError(t, err)
	assert.Equal(t, 0, stats.TotalNodes)
}

// TestGetStats_LeafNodeScope_ReturnsSingleNode verifies stats for a leaf node.
func TestGetStats_LeafNodeScope_ReturnsSingleNode(t *testing.T) {
	s := newTestStore(t)
	seedStatsNodes(t, s)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "STAT-1.2")
	require.NoError(t, err)
	assert.Equal(t, 1, stats.TotalNodes)
	assert.Equal(t, 1, stats.ByStatus["done"])
}

// TestGetStats_WithCancelledNodes_ExcludesFromCount verifies cancelled node handling.
func TestGetStats_WithCancelledNodes_ExcludesFromCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Open",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Cancelled",
		Status: model.StatusCancelled, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalNodes)
	assert.Equal(t, 1, stats.ByStatus["open"])
	assert.Equal(t, 1, stats.ByStatus["cancelled"])
}

// TestGetStats_ProgressWithMixedWeights verifies weighted progress in stats.
func TestGetStats_ProgressWithMixedWeights(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create nodes with different weights and progress.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Heavy Done",
		Status: model.StatusDone, Priority: model.PriorityMedium, Weight: 3.0,
		Progress: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Light Open",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		Progress: 0.0, NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	// Expected: (1.0*3 + 0.0*1) / (3+1) = 0.75
	assert.InDelta(t, 0.75, stats.Progress, 0.001)
}
