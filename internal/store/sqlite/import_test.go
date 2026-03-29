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
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// makeTestExport creates an ExportData with valid checksum for testing.
func makeTestExport(t *testing.T, s *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Import node 1",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "IMP-2", Project: "IMP", Depth: 0, Seq: 2, Title: "Import node 2",
		Status: model.StatusDone, Priority: model.PriorityHigh, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	data, err := s.Export(ctx, "IMP", "0.1.0")
	require.NoError(t, err)
	return data
}

// TestImport_ReplaceMode_MidFailure_RollsBack verifies FR-15.2d:
// if a constraint violation occurs mid-import, the entire transaction
// rolls back and the database remains unchanged.
func TestImport_ReplaceMode_MidFailure_RollsBack(t *testing.T) {
	srcStore := newTestStore(t)
	dstStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node in dstStore that we expect to survive rollback.
	require.NoError(t, dstStore.CreateNode(ctx, &model.Node{
		ID: "KEEP-1", Project: "KEEP", Depth: 0, Seq: 1, Title: "Must survive",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "k1", CreatedAt: now, UpdatedAt: now,
	}))

	// Create export data from src store.
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "NEW-1", Project: "NEW", Depth: 0, Seq: 1, Title: "New node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "n1", CreatedAt: now, UpdatedAt: now,
	}))
	data, err := srcStore.Export(ctx, "NEW", "0.1.0")
	require.NoError(t, err)

	// Inject a dependency referencing a non-existent node to cause FK violation.
	data.Dependencies = append(data.Dependencies, sqlite.MakeExportDep(
		"NEW-1", "NONEXISTENT-99", "blocks", now.Format("2006-01-02T15:04:05Z"),
	))

	// Recalculate checksum to pass validation.
	data.Checksum = sqlite.RecomputeExportChecksum(t, data)

	// Import should fail due to FK constraint.
	_, err = dstStore.Import(ctx, data, sqlite.ImportModeReplace)
	require.Error(t, err, "import should fail on FK constraint violation")

	// Verify rollback: original node must still exist.
	node, getErr := dstStore.GetNode(ctx, "KEEP-1")
	require.NoError(t, getErr, "KEEP-1 should still exist after rollback")
	assert.Equal(t, "Must survive", node.Title)

	// New node must NOT exist.
	_, getErr = dstStore.GetNode(ctx, "NEW-1")
	assert.ErrorIs(t, getErr, model.ErrNotFound, "NEW-1 should not exist after rollback")
}

// TestImport_VerifiesNodeCount_MismatchAborts verifies count validation per FR-7.8.
func TestImport_VerifiesNodeCount_MismatchAborts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	data := &sqlite.ExportData{
		NodeCount: 5, // Wrong count.
		Nodes:     nil,
		Checksum:  "fake",
	}

	_, err := s.Import(ctx, data, sqlite.ImportModeReplace)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "node count mismatch")
}

// TestImport_VerifiesChecksum_MismatchAborts verifies checksum validation per FR-7.8.
func TestImport_VerifiesChecksum_MismatchAborts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	data := &sqlite.ExportData{
		NodeCount: 0,
		Nodes:     nil,
		Checksum:  "invalid_checksum",
	}

	_, err := s.Import(ctx, data, sqlite.ImportModeReplace)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "checksum verification failed")
}

// TestImport_ReplaceMode_DropsAndReimports verifies replace mode per FR-7.8.
func TestImport_ReplaceMode_DropsAndReimports(t *testing.T) {
	// Create source store with data.
	srcStore := newTestStore(t)
	exportData := makeTestExport(t, srcStore)

	// Create destination store with existing data.
	destStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, destStore.CreateNode(ctx, &model.Node{
		ID: "EXISTING-1", Project: "OLD", Depth: 0, Seq: 1, Title: "Old data",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "old", CreatedAt: now, UpdatedAt: now,
	}))

	// Import in replace mode.
	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeReplace)
	require.NoError(t, err)

	assert.Equal(t, 2, result.NodesCreated)
	assert.True(t, result.FTSRebuilt)

	// Verify old data was removed.
	_, err = destStore.GetNode(ctx, "EXISTING-1")
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Verify imported data exists.
	node, err := destStore.GetNode(ctx, "IMP-1")
	require.NoError(t, err)
	assert.Equal(t, "Import node 1", node.Title)
}

// TestImport_MergeMode_ContentHashComparison verifies merge mode per FR-7.8.
func TestImport_MergeMode_ContentHashComparison(t *testing.T) {
	srcStore := newTestStore(t)
	exportData := makeTestExport(t, srcStore)

	// Create dest store with one overlapping node (same hash) and one different.
	destStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Same hash as IMP-1 in export — should be skipped.
	require.NoError(t, destStore.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Import node 1",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeMerge)
	require.NoError(t, err)

	assert.Equal(t, 1, result.NodesCreated, "IMP-2 should be created")
	assert.Equal(t, 1, result.NodesSkipped, "IMP-1 should be skipped (same hash)")
	assert.Equal(t, 0, result.NodesUpdated)
}

// TestImport_MergeMode_UpdatesDifferentHash verifies merge updates changed nodes.
func TestImport_MergeMode_UpdatesDifferentHash(t *testing.T) {
	srcStore := newTestStore(t)
	exportData := makeTestExport(t, srcStore)

	destStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Same ID as IMP-1 but different hash — should be updated.
	require.NoError(t, destStore.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Old title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "different_hash",
		CreatedAt: now, UpdatedAt: now,
	}))

	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeMerge)
	require.NoError(t, err)

	assert.Equal(t, 1, result.NodesUpdated, "IMP-1 should be updated (different hash)")
	assert.Equal(t, 1, result.NodesCreated, "IMP-2 should be created")

	// Verify the node was updated.
	node, err := destStore.GetNode(ctx, "IMP-1")
	require.NoError(t, err)
	assert.Equal(t, "Import node 1", node.Title)
}

// TestImport_FTSRebuiltAfterImport verifies FTS index is rebuilt per FR-7.8.
func TestImport_FTSRebuiltAfterImport(t *testing.T) {
	srcStore := newTestStore(t)
	exportData := makeTestExport(t, srcStore)

	destStore := newTestStore(t)
	ctx := context.Background()

	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.True(t, result.FTSRebuilt)

	// Verify FTS search works on imported data.
	results, total, err := destStore.SearchNodes(ctx, "Import",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "FTS should find both imported nodes")
	assert.Len(t, results, 2)
}

// TestImport_NilData_ReturnsError verifies nil input handling.
func TestImport_NilData_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Import(ctx, nil, sqlite.ImportModeReplace)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestImport_ReplaceMode_WithDependencies_ImportsDeps verifies dep import per FR-7.8.
func TestImport_ReplaceMode_WithDependencies_ImportsDeps(t *testing.T) {
	srcStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create nodes and a dependency in source.
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Node A",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "IMP-2", Project: "IMP", Depth: 0, Seq: 2, Title: "Node B",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))
	require.NoError(t, srcStore.AddDependency(ctx, &model.Dependency{
		FromID: "IMP-1", ToID: "IMP-2", DepType: model.DepTypeRelated,
		CreatedAt: now, CreatedBy: "pm-1",
	}))

	exportData, err := srcStore.Export(ctx, "IMP", "0.1.0")
	require.NoError(t, err)
	require.Len(t, exportData.Dependencies, 1)

	// Import into destination.
	destStore := newTestStore(t)
	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.Equal(t, 2, result.NodesCreated)
	assert.Equal(t, 1, result.DepsImported)
}

// TestImport_MergeMode_WithDependencies_ImportsDeps verifies merge dep import.
func TestImport_MergeMode_WithDependencies_ImportsDeps(t *testing.T) {
	srcStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Node A",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "IMP-2", Project: "IMP", Depth: 0, Seq: 2, Title: "Node B",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))
	require.NoError(t, srcStore.AddDependency(ctx, &model.Dependency{
		FromID: "IMP-1", ToID: "IMP-2", DepType: model.DepTypeRelated,
		CreatedAt: now, CreatedBy: "pm-1",
	}))

	exportData, err := srcStore.Export(ctx, "IMP", "0.1.0")
	require.NoError(t, err)

	destStore := newTestStore(t)
	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeMerge)
	require.NoError(t, err)
	assert.Equal(t, 2, result.NodesCreated)
	assert.Equal(t, 1, result.DepsImported)
}

// TestImport_ReplaceMode_RebuildsFTSAndSequences verifies sequences rebuilt per FR-7.8.
func TestImport_ReplaceMode_RebuildsFTSAndSequences(t *testing.T) {
	srcStore := newTestStore(t)
	exportData := makeTestExport(t, srcStore)

	destStore := newTestStore(t)
	ctx := context.Background()

	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.True(t, result.FTSRebuilt)

	// Verify sequences were rebuilt — NextSequence should return seq > existing max.
	seq, err := destStore.NextSequence(ctx, "IMP:")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, seq, 3, "sequence should be rebuilt from max(seq)")
}

// TestImport_ReplaceMode_EmptyExport_ClearsData verifies empty replace clears DB.
func TestImport_ReplaceMode_EmptyExport_ClearsData(t *testing.T) {
	destStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Seed destination with data.
	require.NoError(t, destStore.CreateNode(ctx, &model.Node{
		ID: "OLD-1", Project: "OLD", Depth: 0, Seq: 1, Title: "Old data",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "old", CreatedAt: now, UpdatedAt: now,
	}))

	// Export from empty source.
	emptyStore := newTestStore(t)
	emptyExport, err := emptyStore.Export(ctx, "EMPTY", "0.1.0")
	require.NoError(t, err)

	result, err := destStore.Import(ctx, emptyExport, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NodesCreated)

	// Old data should be gone.
	_, err = destStore.GetNode(ctx, "OLD-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}
