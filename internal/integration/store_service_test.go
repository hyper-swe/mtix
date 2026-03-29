// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package integration contains end-to-end integration tests per MTIX-11.1.
// Tests use real SQLite databases (temp directories) — no mocks.
package integration

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// testClock returns a fixed-time clock for deterministic tests.
func testClock() func() time.Time {
	fixed := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}

// setupTestEnv creates a real Store and NodeService for integration testing.
func setupTestEnv(t *testing.T) (store.Store, *service.NodeService, context.Context) {
	t.Helper()

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}
	nodeSvc := service.NewNodeService(st, broadcaster, config, logger, testClock())

	return st, nodeSvc, context.Background()
}

// TestIntegration_FullCRUDLifecycle verifies create→read→update→delete→undelete.
func TestIntegration_FullCRUDLifecycle(t *testing.T) {
	st, nodeSvc, ctx := setupTestEnv(t)

	// Create a root node.
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:    "Integration Test Root",
		Project:  "INT",
		Priority: 2,
	})
	require.NoError(t, err)
	require.NotEmpty(t, node.ID)
	assert.Equal(t, "Integration Test Root", node.Title)
	assert.Equal(t, model.StatusOpen, node.Status)

	// Read it back.
	fetched, err := nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, node.ID, fetched.ID)
	assert.Equal(t, node.Title, fetched.Title)

	// Update title.
	newTitle := "Updated Title"
	err = st.UpdateNode(ctx, node.ID, &store.NodeUpdate{Title: &newTitle})
	require.NoError(t, err)

	fetched, err = nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", fetched.Title)

	// Soft delete.
	err = st.DeleteNode(ctx, node.ID, false, "test")
	require.NoError(t, err)

	_, err = nodeSvc.GetNode(ctx, node.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Undelete.
	err = st.UndeleteNode(ctx, node.ID)
	require.NoError(t, err)

	fetched, err = nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", fetched.Title)
}

// TestIntegration_HierarchicalDecomposition verifies parent→children creation.
func TestIntegration_HierarchicalDecomposition(t *testing.T) {
	_, nodeSvc, ctx := setupTestEnv(t)

	// Create root.
	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Root",
		Project: "HIER",
	})
	require.NoError(t, err)

	// Decompose into children.
	childIDs, err := nodeSvc.Decompose(ctx, root.ID, []service.DecomposeInput{
		{Title: "Child A"},
		{Title: "Child B"},
		{Title: "Child C"},
	}, "HIER")
	require.NoError(t, err)
	assert.Len(t, childIDs, 3)

	// Verify parent-child relationship.
	for _, childID := range childIDs {
		child, err := nodeSvc.GetNode(ctx, childID)
		require.NoError(t, err)
		assert.Equal(t, root.ID, child.ParentID)
		assert.Equal(t, model.StatusOpen, child.Status)
	}
}

// TestIntegration_StatusTransitionLifecycle verifies full workflow transitions.
func TestIntegration_StatusTransitionLifecycle(t *testing.T) {
	_, nodeSvc, ctx := setupTestEnv(t)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Workflow Test",
		Project: "WF",
	})
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)

	// open → in_progress
	err = nodeSvc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "claimed", "agent-1")
	require.NoError(t, err)

	// in_progress → done
	err = nodeSvc.TransitionStatus(ctx, node.ID, model.StatusDone, "completed", "agent-1")
	require.NoError(t, err)

	fetched, err := nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, fetched.Status)
}

// TestIntegration_ProgressRollup verifies child completion updates parent progress.
func TestIntegration_ProgressRollup(t *testing.T) {
	st, nodeSvc, ctx := setupTestEnv(t)

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Rollup Parent",
		Project: "ROLL",
	})
	require.NoError(t, err)

	childIDs, err := nodeSvc.Decompose(ctx, root.ID, []service.DecomposeInput{
		{Title: "Task 1"},
		{Title: "Task 2"},
	}, "ROLL")
	require.NoError(t, err)

	// Complete first child (must go through in_progress first per state machine).
	err = nodeSvc.TransitionStatus(ctx, childIDs[0], model.StatusInProgress, "claimed", "test")
	require.NoError(t, err)
	err = nodeSvc.TransitionStatus(ctx, childIDs[0], model.StatusDone, "done", "test")
	require.NoError(t, err)

	// Verify parent progress updated.
	parent, err := st.GetNode(ctx, root.ID)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, parent.Progress, 0.01, "parent should be 50% after 1/2 children done")

	// Complete second child (must go through in_progress first).
	err = nodeSvc.TransitionStatus(ctx, childIDs[1], model.StatusInProgress, "claimed", "test")
	require.NoError(t, err)
	err = nodeSvc.TransitionStatus(ctx, childIDs[1], model.StatusDone, "done", "test")
	require.NoError(t, err)

	parent, err = st.GetNode(ctx, root.ID)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, parent.Progress, 0.01, "parent should be 100% after all children done")
}

// TestIntegration_InvalidTransition_ReturnsError verifies state machine enforcement.
func TestIntegration_InvalidTransition_ReturnsError(t *testing.T) {
	_, nodeSvc, ctx := setupTestEnv(t)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Invalid Trans",
		Project: "INV",
	})
	require.NoError(t, err)

	// open → done should fail (must go through in_progress first).
	err = nodeSvc.TransitionStatus(ctx, node.ID, model.StatusDone, "skip", "test")
	assert.Error(t, err, "open→done should be invalid")
}

// TestIntegration_DependencyBlocking verifies blocking dependencies.
func TestIntegration_DependencyBlocking(t *testing.T) {
	st, nodeSvc, ctx := setupTestEnv(t)

	nodeA, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Blocker",
		Project: "DEP",
	})
	require.NoError(t, err)

	nodeB, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Blocked",
		Project: "DEP",
	})
	require.NoError(t, err)

	// Add blocking dependency: A blocks B (from=blocker, to=blocked node).
	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeBlocks,
	}
	err = st.AddDependency(ctx, dep)
	require.NoError(t, err)

	// Verify blockers — GetBlockers returns deps where to_id matches the blocked node.
	blockers, err := st.GetBlockers(ctx, nodeB.ID)
	require.NoError(t, err)
	assert.Len(t, blockers, 1)
	assert.Equal(t, nodeA.ID, blockers[0].FromID)
}

// TestIntegration_ListNodesWithFilters verifies filtered queries.
func TestIntegration_ListNodesWithFilters(t *testing.T) {
	st, nodeSvc, ctx := setupTestEnv(t)

	// Create nodes with different statuses.
	node1, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title: "Open Task", Project: "FILT",
	})
	require.NoError(t, err)

	node2, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title: "Done Task", Project: "FILT",
	})
	require.NoError(t, err)

	// Transition node2 to done via in_progress.
	err = nodeSvc.TransitionStatus(ctx, node2.ID, model.StatusInProgress, "wip", "test")
	require.NoError(t, err)
	err = nodeSvc.TransitionStatus(ctx, node2.ID, model.StatusDone, "done", "test")
	require.NoError(t, err)

	// Filter by status=open.
	nodes, total, err := st.ListNodes(ctx, store.NodeFilter{
		Status: []model.Status{model.StatusOpen},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)

	assert.GreaterOrEqual(t, total, 1)
	for _, n := range nodes {
		assert.Equal(t, model.StatusOpen, n.Status)
	}

	_ = node1 // Used in creation
}
