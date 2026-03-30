// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// buildRealisticTree creates a 3-level hierarchy for export tests:
// 3 stories × 3 epics × 3 issues = 3 + 9 + 27 = 39 nodes.
func buildRealisticTree(t *testing.T, env *e2eEnv) {
	t.Helper()

	storyTitles := []string{"User Auth", "Payment System", "Notifications"}

	for _, title := range storyTitles {
		story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   title,
			Project: "EXP",
			Creator: "admin",
		})
		require.NoError(t, err)

		epicTitles := []service.DecomposeInput{
			{Title: title + " - Design"},
			{Title: title + " - Backend"},
			{Title: title + " - Tests"},
		}
		epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, epicTitles, "admin")
		require.NoError(t, err)

		for _, epicID := range epicIDs {
			issueTitles := []service.DecomposeInput{
				{Title: "Subtask Alpha"},
				{Title: "Subtask Beta"},
				{Title: "Subtask Gamma"},
			}
			_, err := env.nodeSvc.Decompose(env.ctx, epicID, issueTitles, "admin")
			require.NoError(t, err)
		}
	}
}

// TestE2E_Export_ValidJSON verifies export produces valid JSON.
func TestE2E_Export_ValidJSON(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	// Marshal to JSON — should be valid.
	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)
	assert.Greater(t, len(jsonBytes), 0)

	// Verify it can be parsed back.
	var parsed sqlite.ExportData
	err = json.Unmarshal(jsonBytes, &parsed)
	require.NoError(t, err)
	assert.Equal(t, 1, parsed.Version)
}

// TestE2E_Export_CorrectNodeCount verifies node_count matches actual nodes.
func TestE2E_Export_CorrectNodeCount(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	// 3 stories + 9 epics + 27 issues = 39.
	assert.Equal(t, 39, data.NodeCount)
	assert.Len(t, data.Nodes, 39)
}

// TestE2E_Export_ContentHash verifies export checksum is valid.
func TestE2E_Export_ContentHash(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	assert.NotEmpty(t, data.Checksum)
	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid, "export checksum should be valid")
}

// TestE2E_Import_AllNodesRestored verifies import restores all nodes.
func TestE2E_Import_AllNodesRestored(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	// Export from source.
	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	// Create a fresh database for import target.
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "import-target")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	target, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	// Import into fresh database.
	result, err := target.Import(env.ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	assert.Equal(t, 39, result.NodesCreated)
	assert.True(t, result.FTSRebuilt)
}

// TestE2E_Import_TreeStructurePreserved verifies parent-child relationships survive import.
func TestE2E_Import_TreeStructurePreserved(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	// Import into fresh database.
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "import-tree")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	target, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	_, err = target.Import(env.ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	// Verify tree structure by counting children at each depth.
	nodes, _, err := target.ListNodes(env.ctx, store.NodeFilter{}, store.ListOptions{Limit: 100})
	require.NoError(t, err)

	depthCounts := map[int]int{}
	for _, n := range nodes {
		depthCounts[n.Depth]++
	}
	assert.Equal(t, 3, depthCounts[0], "3 stories at depth 0")
	assert.Equal(t, 9, depthCounts[1], "9 epics at depth 1")
	assert.Equal(t, 27, depthCounts[2], "27 issues at depth 2")
}

// TestE2E_Import_DependenciesPreserved verifies dependencies survive import.
func TestE2E_Import_DependenciesPreserved(t *testing.T) {
	env := setupE2E(t)

	// Create two nodes with a dependency.
	nodeA, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Node A", Project: "DEP", Creator: "admin",
	})
	require.NoError(t, err)
	nodeB, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Node B", Project: "DEP", Creator: "admin",
	})
	require.NoError(t, err)

	dep := &model.Dependency{
		FromID:  nodeB.ID,
		ToID:    nodeA.ID,
		DepType: model.DepTypeBlocks,
	}
	err = env.store.AddDependency(env.ctx, dep)
	require.NoError(t, err)

	// Export.
	data, err := env.sqlStore.Export(env.ctx, "DEP", "0.1.0")
	require.NoError(t, err)
	assert.Len(t, data.Dependencies, 1)

	// Import into fresh database.
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "import-deps")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	target, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	_, err = target.Import(env.ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	// Verify dependency exists in target.
	// The dep is FromID=nodeB, ToID=nodeA. GetBlockers queries WHERE to_id = ?,
	// so pass nodeA.ID to find the dependency where nodeA is the target.
	blockers, err := target.GetBlockers(env.ctx, nodeA.ID)
	require.NoError(t, err)
	assert.Len(t, blockers, 1)
	assert.Equal(t, nodeA.ID, blockers[0].ToID)
}

// TestE2E_Import_SequencesRebuilt verifies sequences work after import.
func TestE2E_Import_SequencesRebuilt(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	// Import into fresh database.
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "import-seq")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	target, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	_, err = target.Import(env.ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	// Verify we can create new nodes without ID collision.
	targetSvc := service.NewNodeService(target, &service.NoopBroadcaster{},
		&service.StaticConfig{}, nil, testClock())

	newNode, err := targetSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "New Node After Import",
		Project: "EXP",
		Creator: "admin",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, newNode.ID)

	// Verify the new node's ID doesn't conflict with imported IDs.
	for _, n := range data.Nodes {
		assert.NotEqual(t, n.ID, newNode.ID,
			"new node ID should not collide with imported ID %s", n.ID)
	}
}

// TestE2E_Import_FTSRebuild verifies FTS search works after import.
func TestE2E_Import_FTSRebuild(t *testing.T) {
	env := setupE2E(t)
	buildRealisticTree(t, env)

	data, err := env.sqlStore.Export(env.ctx, "EXP", "0.1.0")
	require.NoError(t, err)

	// Import into fresh database.
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "import-fts")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	target, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	_, err = target.Import(env.ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	// FTS search should work on imported data.
	results, total, err := target.SearchNodes(env.ctx, "Payment",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Greater(t, total, 0, "FTS should find 'Payment' in imported data")
	assert.Greater(t, len(results), 0)
}

// TestE2E_Import_MergeMode_Deduplication verifies merge mode handles duplicates.
func TestE2E_Import_MergeMode_Deduplication(t *testing.T) {
	env := setupE2E(t)

	// Create a small tree.
	_, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Merge Node",
		Project: "MRG",
		Creator: "admin",
	})
	require.NoError(t, err)

	data, err := env.sqlStore.Export(env.ctx, "MRG", "0.1.0")
	require.NoError(t, err)

	// Import into the SAME database with merge mode.
	result, err := env.sqlStore.Import(env.ctx, data, sqlite.ImportModeMerge, false)
	require.NoError(t, err)

	// In merge mode, existing nodes with matching content hash should be skipped.
	assert.Equal(t, 0, result.NodesCreated,
		"merge should not create duplicates for identical nodes")
	assert.GreaterOrEqual(t, result.NodesSkipped, 1,
		"merge should skip existing nodes")
}

// TestE2E_Import_NewIDsNoCollision verifies new nodes don't collide after import.
func TestE2E_Import_NewIDsNoCollision(t *testing.T) {
	env := setupE2E(t)

	// Create initial nodes.
	for i := 0; i < 5; i++ {
		_, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   "Pre-import Node",
			Project: "COL",
			Creator: "admin",
		})
		require.NoError(t, err)
	}

	data, err := env.sqlStore.Export(env.ctx, "COL", "0.1.0")
	require.NoError(t, err)

	// Import into fresh database.
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "import-collision")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	target, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })

	_, err = target.Import(env.ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	// Create additional nodes — they should get unique IDs.
	targetSvc := service.NewNodeService(target, &service.NoopBroadcaster{},
		&service.StaticConfig{}, nil, testClock())

	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		node, err := targetSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   "Post-import Node",
			Project: "COL",
			Creator: "admin",
		})
		require.NoError(t, err)
		assert.False(t, ids[node.ID], "node ID %s should be unique", node.ID)
		ids[node.ID] = true
	}
}
