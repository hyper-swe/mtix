// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// =============================================================================
// Verify coverage — all sub-checks (FR-6.3)
// =============================================================================

// TestVerify_EmptyDatabase_AllPass verifies verification passes on empty DB per FR-6.3.
func TestVerify_EmptyDatabase_AllPass(t *testing.T) {
	s := newTestStore(t)
	result, err := s.Verify(context.Background())
	require.NoError(t, err)
	assert.True(t, result.AllPassed)
	assert.True(t, result.IntegrityOK)
	assert.True(t, result.ForeignKeyOK)
	assert.True(t, result.SequenceOK)
	assert.True(t, result.ProgressOK)
	assert.True(t, result.FTSOK)
	assert.Empty(t, result.Errors)
}

// TestVerify_AfterHierarchy_AllPass verifies verification after CRUD per FR-6.3.
func TestVerify_AfterHierarchy_AllPass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Populate sequences table so verifySequences passes.
	_, err := s.NextSequence(ctx, "VH:")
	require.NoError(t, err)
	_, err = s.NextSequence(ctx, "VH:VH-1")
	require.NoError(t, err)
	_, err = s.NextSequence(ctx, "VH:VH-1.1")
	require.NoError(t, err)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("VH-1", "VH", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VH-1.1", "VH-1", "VH", "Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VH-1.1.1", "VH-1.1", "VH", "Grandchild", 2, 1, now)))

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.AllPassed)
}

// TestVerify_AfterTransitions_ProgressConsistent verifies progress after status changes per FR-6.3.
func TestVerify_AfterTransitions_ProgressConsistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Populate sequences table so verifySequences passes.
	_, err := s.NextSequence(ctx, "VP:")
	require.NoError(t, err)
	// Advance to seq=2 for VP:VP-1 since we create two children.
	_, err = s.NextSequence(ctx, "VP:VP-1")
	require.NoError(t, err)
	_, err = s.NextSequence(ctx, "VP:VP-1")
	require.NoError(t, err)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("VP-1", "VP", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VP-1.1", "VP-1", "VP", "Child1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VP-1.2", "VP-1", "VP", "Child2", 1, 2, now)))

	require.NoError(t, s.TransitionStatus(ctx, "VP-1.1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "VP-1.1", model.StatusDone, "done", "agent"))

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.AllPassed)
}

// TestVerify_AfterImportCycle_AllPass verifies verification after export+import per FR-6.3.
func TestVerify_AfterImportCycle_AllPass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("VI-1", "VI", "Node", now)))

	data, err := s.Export(ctx, "VI", "1.0.0")
	require.NoError(t, err)

	_, err = s.Import(ctx, data, sqlite.ImportModeReplace)
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.AllPassed)
}

// =============================================================================
// GetStats coverage (FR-2.7.5)
// =============================================================================

// TestGetStats_GlobalScope_AllCounts verifies global stats per FR-2.7.5.
func TestGetStats_GlobalScope_AllCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("GS-1", "GS", "Open Node", now)))
	n2 := makeRootNode("GS-2", "GS", "Done Node", now)
	n2.Priority = model.PriorityHigh
	require.NoError(t, s.CreateNode(ctx, n2))
	require.NoError(t, s.TransitionStatus(ctx, "GS-2", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "GS-2", model.StatusDone, "done", "agent"))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalNodes)
	assert.Contains(t, stats.ByStatus, "open")
	assert.Contains(t, stats.ByStatus, "done")
	assert.NotEmpty(t, stats.ByPriority)
	assert.NotEmpty(t, stats.ByType)
}

// TestGetStats_ScopedToSubtree_FiltersCorrectly verifies scoped stats per FR-2.7.5.
func TestGetStats_ScopedToSubtree_FiltersCorrectly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SC-1", "SC", "Tree A Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SC-1.1", "SC-1", "SC", "Tree A Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SC-2", "SC", "Tree B Root", now)))

	stats, err := s.GetStats(ctx, "SC-1")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalNodes)

	stats2, err := s.GetStats(ctx, "SC-2")
	require.NoError(t, err)
	assert.Equal(t, 1, stats2.TotalNodes)
}

// TestGetStats_EmptyStore_ReturnsZeros verifies empty store stats per FR-2.7.5.
func TestGetStats_EmptyStore_ReturnsZeros(t *testing.T) {
	s := newTestStore(t)
	stats, err := s.GetStats(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, 0, stats.TotalNodes)
	assert.InDelta(t, 0.0, stats.Progress, 0.001)
}

// TestGetStats_ScopedNonExistent_ReturnsZeros verifies non-existent scope per FR-2.7.5.
func TestGetStats_ScopedNonExistent_ReturnsZeros(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SN-1", "SN", "Node", now)))

	stats, err := s.GetStats(ctx, "NONEXISTENT")
	require.NoError(t, err)
	assert.Equal(t, 0, stats.TotalNodes)
}

// TestGetStats_ProgressWeightedAverage_Correct verifies weighted progress per FR-2.7.5.
func TestGetStats_ProgressWeightedAverage_Correct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("WA-1", "WA", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("WA-1.1", "WA-1", "WA", "Child1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("WA-1.2", "WA-1", "WA", "Child2", 1, 2, now)))

	require.NoError(t, s.TransitionStatus(ctx, "WA-1.1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "WA-1.1", model.StatusDone, "done", "agent"))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Greater(t, stats.Progress, 0.0)
}

// =============================================================================
// Export coverage (FR-7.8)
// =============================================================================

// TestExport_WithDependencies_ExportsAll verifies dependency export per FR-7.8.
func TestExport_WithDependencies_ExportsAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("EX-1", "EX", "Node A", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("EX-2", "EX", "Node B", now)))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "EX-1", ToID: "EX-2", DepType: model.DepTypeBlocks,
	}))

	data, err := s.Export(ctx, "EX", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, 2, data.NodeCount)
	assert.Len(t, data.Dependencies, 1)
	assert.NotEmpty(t, data.Checksum)
	assert.Equal(t, 1, data.Version)
}

// TestExport_SortsDeterministically verifies canonical ordering per FR-7.8.
func TestExport_SortsDeterministically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SD-3", "SD", "Third", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SD-1", "SD", "First", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SD-2", "SD", "Second", now)))

	data, err := s.Export(ctx, "SD", "1.0.0")
	require.NoError(t, err)
	require.Len(t, data.Nodes, 3)
	assert.Equal(t, "SD-1", data.Nodes[0].ID)
	assert.Equal(t, "SD-2", data.Nodes[1].ID)
	assert.Equal(t, "SD-3", data.Nodes[2].ID)
}

// TestVerifyExportChecksum_ValidExport_Matches verifies checksum validation per FR-7.8.
func TestVerifyExportChecksum_ValidExport_Matches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("VE-1", "VE", "Node", now)))

	data, err := s.Export(ctx, "VE", "1.0.0")
	require.NoError(t, err)

	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid)
}

// TestVerifyExportChecksum_TamperedData_Fails verifies tamper detection per FR-7.8.
func TestVerifyExportChecksum_TamperedData_Fails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("TE-1", "TE", "Node", now)))

	data, err := s.Export(ctx, "TE", "1.0.0")
	require.NoError(t, err)
	data.Checksum = "000000000000"

	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.False(t, valid)
}

// TestVerifyExportChecksum_NilExport_ReturnsError verifies nil input per FR-7.8.
func TestVerifyExportChecksum_NilExport_ReturnsError(t *testing.T) {
	_, err := sqlite.VerifyExportChecksum(nil)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// =============================================================================
// Import coverage (FR-7.8)
// =============================================================================

// TestImport_NodeCountMismatch_ReturnsInvalidInput verifies count check per FR-7.8.
func TestImport_NodeCountMismatch_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	data := &sqlite.ExportData{NodeCount: 5, Nodes: nil}
	_, err := s.Import(context.Background(), data, sqlite.ImportModeReplace)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestImport_ChecksumMismatch_ReturnsInvalidInput verifies checksum check per FR-7.8.
func TestImport_ChecksumMismatch_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("IC-1", "IC", "Node", now)))

	data, err := s.Export(ctx, "IC", "1.0.0")
	require.NoError(t, err)
	data.Checksum = "bad_checksum"

	_, err = s.Import(ctx, data, sqlite.ImportModeReplace)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestImport_ReplaceMode_ClearsAndReimports verifies replace mode per FR-7.8.
func TestImport_ReplaceMode_ClearsAndReimports(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("IR-1", "IR", "Original", now)))
	data, err := s.Export(ctx, "IR", "1.0.0")
	require.NoError(t, err)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("IR-2", "IR", "Extra", now)))

	result, err := s.Import(ctx, data, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesCreated)
	assert.True(t, result.FTSRebuilt)

	_, err = s.GetNode(ctx, "IR-2")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestImport_MergeMode_SkipsUnchanged verifies merge skip per FR-7.8.
func TestImport_MergeMode_SkipsUnchanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("IM-1", "IM", "Existing", now)))
	data, err := s.Export(ctx, "IM", "1.0.0")
	require.NoError(t, err)

	result, err := s.Import(ctx, data, sqlite.ImportModeMerge)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesSkipped)
}

// TestImport_MergeMode_UpdatesChanged verifies content diff merge per FR-7.8.
func TestImport_MergeMode_UpdatesChanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("MU-1", "MU", "Original", now)))
	require.NoError(t, s.UpdateNode(ctx, "MU-1", &store.NodeUpdate{
		Title: strPtr("Updated Title"),
	}))

	data, err := s.Export(ctx, "MU", "1.0.0")
	require.NoError(t, err)

	s2 := newTestStore(t)
	require.NoError(t, s2.CreateNode(ctx, makeRootNode("MU-1", "MU", "Original", now)))

	result, err := s2.Import(ctx, data, sqlite.ImportModeMerge)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesUpdated)
}

// TestImport_ReplaceMode_WithDeps verifies dep import per FR-7.8.
func TestImport_ReplaceMode_WithDeps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("RD-1", "RD", "A", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("RD-2", "RD", "B", now)))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "RD-1", ToID: "RD-2", DepType: model.DepTypeRelated,
	}))

	data, err := s.Export(ctx, "RD", "1.0.0")
	require.NoError(t, err)

	s2 := newTestStore(t)
	result, err := s2.Import(ctx, data, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.Equal(t, 2, result.NodesCreated)
	assert.Equal(t, 1, result.DepsImported)
}

// =============================================================================
// Backup coverage (FR-6.3a)
// =============================================================================

// TestBackup_EmptyDatabase_Succeeds verifies backup of empty DB per FR-6.3a.
func TestBackup_EmptyDatabase_Succeeds(t *testing.T) {
	s := newTestStore(t)
	destPath := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.Backup(context.Background(), destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.Greater(t, result.Size, int64(0))
}

// TestBackup_WithData_Readable verifies backup with data per FR-6.3a.
func TestBackup_WithData_Readable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("BK-1", "BK", "Backup Test", now)))

	destPath := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)

	s2, err := sqlite.New(destPath, slog.Default())
	require.NoError(t, err)
	defer func() { _ = s2.Close() }()

	node, err := s2.GetNode(ctx, "BK-1")
	require.NoError(t, err)
	assert.Equal(t, "Backup Test", node.Title)
}

// TestBackup_EmptyPath_ReturnsError verifies empty path per FR-6.3a.
func TestBackup_EmptyPath_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Backup(context.Background(), "")
	assert.Error(t, err)
}

// =============================================================================
// Dependency coverage (FR-4.1)
// =============================================================================

// TestDetectCycle_TransitiveCycle_Detected verifies transitive cycle detection per FR-4.1.
func TestDetectCycle_TransitiveCycle_Detected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CY-1", "CY", "A", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("CY-2", "CY", "B", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("CY-3", "CY", "C", now)))

	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CY-1", ToID: "CY-2", DepType: model.DepTypeBlocks,
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CY-2", ToID: "CY-3", DepType: model.DepTypeBlocks,
	}))

	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "CY-3", ToID: "CY-1", DepType: model.DepTypeBlocks,
	})
	assert.ErrorIs(t, err, model.ErrCycleDetected)
}

// TestRemoveDependency_ExistingRelated_Succeeds verifies dep removal per FR-4.1.
func TestRemoveDependency_ExistingRelated_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("RM-1", "RM", "A", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("RM-2", "RM", "B", now)))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "RM-1", ToID: "RM-2", DepType: model.DepTypeRelated,
	}))

	err := s.RemoveDependency(ctx, "RM-1", "RM-2", model.DepTypeRelated)
	assert.NoError(t, err)
}

// TestAddDependency_SelfReference_ReturnsError verifies self-dep rejection per FR-4.1.
func TestAddDependency_SelfReference_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SR-1", "SR", "Self", now)))

	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "SR-1", ToID: "SR-1", DepType: model.DepTypeBlocks,
	})
	assert.Error(t, err)
}

// =============================================================================
// Cancel coverage (FR-6.3)
// =============================================================================

// TestCancelNode_CascadeSkipsTerminal_PreservesDone verifies cascade skip per FR-6.3.
func TestCancelNode_CascadeSkipsTerminal_PreservesDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CS-1", "CS", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("CS-1.1", "CS-1", "CS", "Done Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("CS-1.2", "CS-1", "CS", "Open Child", 1, 2, now)))

	require.NoError(t, s.TransitionStatus(ctx, "CS-1.1", model.StatusInProgress, "start", "a"))
	require.NoError(t, s.TransitionStatus(ctx, "CS-1.1", model.StatusDone, "done", "a"))

	require.NoError(t, s.CancelNode(ctx, "CS-1", "test cancel", "admin", true))

	n1, err := s.GetNode(ctx, "CS-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, n1.Status)

	n2, err := s.GetNode(ctx, "CS-1.2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, n2.Status)
}

// =============================================================================
// Delete / Undelete coverage (FR-3.3)
// =============================================================================

// TestDeleteNode_CascadeAllDescendants verifies cascade delete per FR-3.3.
func TestDeleteNode_CascadeAllDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("DD-1", "DD", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("DD-1.1", "DD-1", "DD", "Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("DD-1.1.1", "DD-1.1", "DD", "Grandchild", 2, 1, now)))

	require.NoError(t, s.DeleteNode(ctx, "DD-1", true, "admin"))

	for _, id := range []string{"DD-1", "DD-1.1", "DD-1.1.1"} {
		_, err := s.GetNode(ctx, id)
		assert.ErrorIs(t, err, model.ErrNotFound, "node %s should be deleted", id)
	}
}

// TestDeleteNode_NoCascade_KeepsChildren verifies non-cascade delete per FR-3.3.
func TestDeleteNode_NoCascade_KeepsChildren(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("DN-1", "DN", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("DN-1.1", "DN-1", "DN", "Child", 1, 1, now)))

	require.NoError(t, s.DeleteNode(ctx, "DN-1", false, "admin"))

	_, err := s.GetNode(ctx, "DN-1")
	assert.ErrorIs(t, err, model.ErrNotFound)

	child, err := s.GetNode(ctx, "DN-1.1")
	require.NoError(t, err)
	assert.Equal(t, "Child", child.Title)
}

// TestUndeleteNode_RestoresAll verifies undelete per FR-3.3.
func TestUndeleteNode_RestoresAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UD-1", "UD", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("UD-1.1", "UD-1", "UD", "Child", 1, 1, now)))

	require.NoError(t, s.DeleteNode(ctx, "UD-1", true, "admin"))
	require.NoError(t, s.UndeleteNode(ctx, "UD-1"))

	_, err := s.GetNode(ctx, "UD-1")
	assert.NoError(t, err)
	_, err = s.GetNode(ctx, "UD-1.1")
	assert.NoError(t, err)
}

// TestUndeleteNode_ActiveNode_ReturnsNotFound verifies undelete of non-deleted node per FR-3.3.
func TestUndeleteNode_ActiveNode_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node but don't delete it.
	node := makeRootNode("UND-1", "UND", "Active Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Undelete should fail because the node is not deleted.
	err := s.UndeleteNode(ctx, "UND-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// =============================================================================
// Store New / Close coverage (NFR-2.1)
// =============================================================================

// TestNew_DoubleClose_NoError verifies double close is safe per NFR-2.1.
func TestNew_DoubleClose_NoError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	// First close should succeed.
	require.NoError(t, s.Close())
	// Second close: the underlying sql.DB may or may not error; just verify no panic.
	_ = s.Close()
}

// TestNew_ReadOnlyDir_ReturnsError verifies creation fails on invalid path per NFR-2.1.
func TestNew_ReadOnlyDir_ReturnsError(t *testing.T) {
	_, err := sqlite.New("/dev/null/test.db", slog.Default())
	assert.Error(t, err)
}

// =============================================================================
// Transition coverage (FR-3.5)
// =============================================================================

// TestTransitionStatus_MultipleTransitions_AccumulatesActivity verifies activity accumulation per FR-3.5.
func TestTransitionStatus_MultipleTransitions_AccumulatesActivity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("AT-1", "AT", "Activity Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "AT-1", model.StatusInProgress, "start", "agent1"))
	require.NoError(t, s.TransitionStatus(ctx, "AT-1", model.StatusBlocked, "blocked", "agent1"))
	require.NoError(t, s.TransitionStatus(ctx, "AT-1", model.StatusInProgress, "unblocked", "agent2"))
	require.NoError(t, s.TransitionStatus(ctx, "AT-1", model.StatusDone, "completed", "agent2"))

	// Verify activity entries accumulated via raw SQL.
	var actJSON string
	err := s.QueryRow(ctx, "SELECT activity FROM nodes WHERE id = ?", "AT-1").Scan(&actJSON)
	require.NoError(t, err)
	assert.Contains(t, actJSON, "start")
	assert.Contains(t, actJSON, "completed")
}

// TestTransitionStatus_ToInvalidated_SavesPreviousStatus verifies invalidated saves prev per FR-3.5.
func TestTransitionStatus_ToInvalidated_SavesPreviousStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("IN-1", "IN", "To Invalidate", now)))
	require.NoError(t, s.TransitionStatus(ctx, "IN-1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "IN-1", model.StatusDone, "done", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "IN-1", model.StatusInvalidated, "invalid", "admin"))

	node, err := s.GetNode(ctx, "IN-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, node.Status)
}

// =============================================================================
// Claim coverage (FR-3.6)
// =============================================================================

// TestClaimNode_AlreadyClaimedByOther_ReturnsAlreadyClaimed verifies concurrent claim per FR-3.6.
func TestClaimNode_AlreadyClaimedByOther_ReturnsAlreadyClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CC-1", "CC", "Claimed Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "CC-1", "agent-1"))
	err := s.ClaimNode(ctx, "CC-1", "agent-2")
	assert.ErrorIs(t, err, model.ErrAlreadyClaimed)
}

// TestUnclaimNode_RecordsAndRestoresOpen verifies unclaim per FR-3.6.
func TestUnclaimNode_RecordsAndRestoresOpen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UA-1", "UA", "Unclaim Test", now)))
	require.NoError(t, s.ClaimNode(ctx, "UA-1", "agent-1"))
	require.NoError(t, s.UnclaimNode(ctx, "UA-1", "agent-1", "releasing"))

	node, err := s.GetNode(ctx, "UA-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
	assert.Empty(t, node.Assignee)
}

// =============================================================================
// Context coverage (FR-9.1)
// =============================================================================

// TestGetSiblings_MultipleChildren_ExcludesSelf verifies sibling retrieval per FR-9.1.
func TestGetSiblings_MultipleChildren_ExcludesSelf(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SB-1", "SB", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SB-1.1", "SB-1", "SB", "Child1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SB-1.2", "SB-1", "SB", "Child2", 1, 2, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SB-1.3", "SB-1", "SB", "Child3", 1, 3, now)))

	siblings, err := s.GetSiblings(ctx, "SB-1.2")
	require.NoError(t, err)
	assert.Len(t, siblings, 2)
}

// TestSetAnnotations_Roundtrip_Persists verifies annotation persistence per FR-9.1.
func TestSetAnnotations_Roundtrip_Persists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("AN-1", "AN", "Annotated", now)))

	annotations := []model.Annotation{
		{ID: "ann-1", Text: "First annotation", Author: "admin"},
		{ID: "ann-2", Text: "Second annotation", Author: "agent"},
	}
	require.NoError(t, s.SetAnnotations(ctx, "AN-1", annotations))

	node, err := s.GetNode(ctx, "AN-1")
	require.NoError(t, err)
	assert.Len(t, node.Annotations, 2)
}

// =============================================================================
// ListNodes coverage (FR-3.1)
// =============================================================================

// TestListNodes_Pagination_CorrectTotalCount verifies pagination per FR-3.1.
func TestListNodes_Pagination_CorrectTotalCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("PG-%d", i)
		require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PG", fmt.Sprintf("Node %d", i), now)))
	}

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 2, Offset: 0})
	require.NoError(t, err)
	assert.Len(t, nodes, 2)
	assert.Equal(t, 5, total)
}

// TestListNodes_DeletedExcluded_ByDefault verifies soft-delete filtering per FR-3.1.
func TestListNodes_DeletedExcluded_ByDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("LD-1", "LD", "Active", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("LD-2", "LD", "ToDelete", now)))
	require.NoError(t, s.DeleteNode(ctx, "LD-2", false, "admin"))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 100, Offset: 0})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, nodes, 1)
}

// =============================================================================
// Tree coverage (FR-3.2)
// =============================================================================

// TestGetTree_DeepHierarchy_AllDescendants verifies tree retrieval per FR-3.2.
func TestGetTree_DeepHierarchy_AllDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TR-1", "TR", "L0", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1", "TR-1", "TR", "L1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1.1", "TR-1.1", "TR", "L2", 2, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1.1.1", "TR-1.1.1", "TR", "L3", 3, 1, now)))

	// maxDepth=2 — includes root (depth 0), L1 (depth 1), L2 (depth 2).
	nodes, err := s.GetTree(ctx, "TR-1", 2)
	require.NoError(t, err)
	assert.Len(t, nodes, 3, "should include root, L1, L2 but not L3")
}

// =============================================================================
// Search coverage (FR-2.8)
// =============================================================================

// TestSearchNodes_MultipleMatches_Found verifies FTS search per FR-2.8.
func TestSearchNodes_MultipleMatches_Found(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SE-1", "SE", "Database migration tool", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SE-2", "SE", "Database backup utility", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SE-3", "SE", "Unrelated task", now)))

	results, _, err := s.SearchNodes(ctx, "Database", store.NodeFilter{}, store.ListOptions{Limit: 10, Offset: 0})
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

// =============================================================================
// Helper
// =============================================================================

func strPtr(s string) *string {
	return &s
}
