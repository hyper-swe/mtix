// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
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
// Store / Close / Init coverage
// =============================================================================

// TestClose_AfterOperations_ShutsDownCleanly verifies close after operations.
func TestClose_AfterOperations_ShutsDownCleanly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)

	// Perform some operations before closing.
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-1", "CL", "Node", now)))
	_, err = s.GetNode(ctx, "CL-1")
	require.NoError(t, err)

	err = s.Close()
	assert.NoError(t, err, "close after operations should succeed")
}

// TestNew_InvalidPath_ReturnsError verifies creation with invalid path.
func TestNew_InvalidPath_ReturnsError(t *testing.T) {
	// A path nested under a non-existent dir.
	_, err := sqlite.New("/nonexistent/deeply/nested/path/test.db", slog.Default())
	assert.Error(t, err, "should fail with invalid path")
}

// TestNew_SchemaIdempotency_MultipleInits verifies re-initializing existing DB.
func TestNew_SchemaIdempotency_MultipleInits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// First init.
	s1, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)

	// Create some data.
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s1.CreateNode(ctx, makeRootNode("INIT-1", "INIT", "Test", now)))
	require.NoError(t, s1.Close())

	// Second init on same DB — data should persist.
	s2, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s2.Close()) }()

	node, err := s2.GetNode(ctx, "INIT-1")
	require.NoError(t, err)
	assert.Equal(t, "Test", node.Title)
}

// =============================================================================
// childCount coverage (was 0%)
// =============================================================================

// TestGetDirectChildren_MultipleChildren_CountMatches verifies child count.
func TestGetDirectChildren_MultipleChildren_CountMatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("CC-1", "CC", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	for i := 1; i <= 5; i++ {
		child := makeChildNode(
			fmt.Sprintf("CC-1.%d", i), "CC-1", "CC",
			fmt.Sprintf("Child %d", i), 1, i, now,
		)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	children, err := s.GetDirectChildren(ctx, "CC-1")
	require.NoError(t, err)
	assert.Len(t, children, 5)
}

// =============================================================================
// Verify coverage: verifyForeignKeys, verifyFTS, verifySequences, verifyProgress
// =============================================================================

// TestVerify_WithMultipleNodes_AllPass verifies comprehensive check on real data.
func TestVerify_WithMultipleNodes_AllPass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Build a multi-level tree.
	require.NoError(t, s.CreateNode(ctx, makeRootNode("VF-1", "VF", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VF-1.1", "VF-1", "VF", "Child 1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VF-1.2", "VF-1", "VF", "Child 2", 1, 2, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("VF-1.1.1", "VF-1.1", "VF", "Grandchild", 2, 1, now)))

	// Ensure sequences match.
	_, err := s.NextSequence(ctx, "VF:")
	require.NoError(t, err)
	_, err = s.NextSequence(ctx, "VF:VF-1")
	require.NoError(t, err)
	_, err = s.NextSequence(ctx, "VF:VF-1")
	require.NoError(t, err)
	_, err = s.NextSequence(ctx, "VF:VF-1.1")
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.IntegrityOK)
	assert.True(t, result.ForeignKeyOK)
	assert.True(t, result.FTSOK)
	assert.True(t, result.ProgressOK)
}

// TestVerify_ProgressInconsistency_MultipleChildren_Detected verifies with weights.
func TestVerify_ProgressInconsistency_MultipleChildren_Detected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "VP-1", Project: "VP", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Child with progress 0.5.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "VP-1.1", ParentID: "VP-1", Project: "VP", Depth: 1, Seq: 1,
		Title: "Child A", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, Progress: 0.5, NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Child with progress 0.0.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "VP-1.2", ParentID: "VP-1", Project: "VP", Depth: 1, Seq: 2,
		Title: "Child B", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 2.0, Progress: 0.0, NodeType: model.NodeTypeIssue, ContentHash: "h3",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Corrupt progress manually.
	_, err := s.WriteDB().ExecContext(ctx, "UPDATE nodes SET progress = 0.99 WHERE id = 'VP-1'")
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.False(t, result.ProgressOK)
	assert.False(t, result.AllPassed)

	// Verify error message content.
	found := false
	for _, e := range result.Errors {
		if e != "" {
			found = true
		}
	}
	assert.True(t, found)
}

// =============================================================================
// Backup: escapeSQLitePath (path with single quotes)
// =============================================================================

// TestBackup_PathWithSingleQuote_Succeeds verifies paths with quotes work.
func TestBackup_PathWithSingleQuote_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create a dir with a quote in the name.
	dir := t.TempDir()
	destPath := filepath.Join(dir, "it's_a_backup.db")

	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.True(t, result.Size > 0)
}

// =============================================================================
// Export: agents, sessions, and complete coverage
// =============================================================================

// TestExport_EmptyAgentsAndSessions_ReturnsEmpty verifies nil/empty arrays.
func TestExport_EmptyAgentsAndSessions_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	data, err := s.Export(ctx, "EMPTY", "0.1.0")
	require.NoError(t, err)

	assert.Empty(t, data.Agents)
	assert.Empty(t, data.Sessions)
	assert.Empty(t, data.Dependencies)
}

// TestExport_MultipleSessionsAndAgents_ExportsAll verifies multiple agents/sessions.
func TestExport_MultipleSessionsAndAgents_ExportsAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	nowStr := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)

	// Insert multiple agents.
	for i := 1; i <= 3; i++ {
		_, err := s.WriteDB().ExecContext(ctx,
			`INSERT INTO agents (agent_id, project, state, last_heartbeat) VALUES (?, ?, ?, ?)`,
			fmt.Sprintf("agent-%d", i), "EX", "idle", nowStr,
		)
		require.NoError(t, err)
	}

	// Insert multiple sessions.
	for i := 1; i <= 2; i++ {
		_, err := s.WriteDB().ExecContext(ctx,
			`INSERT INTO sessions (id, agent_id, project, started_at, status, ended_at, summary) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("sess-%d", i), "agent-1", "EX", nowStr, "ended", nowStr, "Summary",
		)
		require.NoError(t, err)
	}

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	assert.Len(t, data.Agents, 3)
	assert.Len(t, data.Sessions, 2)

	// Verify fields are populated.
	assert.Equal(t, "agent-1", data.Agents[0].AgentID)
	assert.Equal(t, "sess-1", data.Sessions[0].ID)
	assert.Equal(t, "ended", data.Sessions[0].Status)
	assert.NotEmpty(t, data.Sessions[0].EndedAt)
	assert.Equal(t, "Summary", data.Sessions[0].Summary)
}

// TestVerifyExportChecksum_EmptyExport_Succeeds verifies checksum of empty data.
func TestVerifyExportChecksum_EmptyExport_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	data, err := s.Export(ctx, "E", "0.1.0")
	require.NoError(t, err)

	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid)
}

// =============================================================================
// Import: merge mode with different hash, rebuild sequences, FTS
// =============================================================================

// TestImport_MergeMode_ExistingNodeNullHash_Updates verifies update when hash is NULL.
func TestImport_MergeMode_ExistingNodeNullHash_Updates(t *testing.T) {
	srcStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Source node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	exportData, err := srcStore.Export(ctx, "IMP", "0.1.0")
	require.NoError(t, err)

	destStore := newTestStore(t)

	// Create dest node with NULL content_hash.
	require.NoError(t, destStore.CreateNode(ctx, &model.Node{
		ID: "IMP-1", Project: "IMP", Depth: 0, Seq: 1, Title: "Old title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "", CreatedAt: now, UpdatedAt: now,
	}))
	// Force content_hash to NULL.
	_, err = destStore.WriteDB().ExecContext(ctx,
		"UPDATE nodes SET content_hash = NULL WHERE id = 'IMP-1'")
	require.NoError(t, err)

	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeMerge)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesUpdated, "should update node with NULL hash")
}

// TestImport_ReplaceMode_WithMultipleSequenceGroups_Rebuilds verifies sequence rebuild.
func TestImport_ReplaceMode_WithMultipleSequenceGroups_Rebuilds(t *testing.T) {
	srcStore := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create parent with multiple children — generates multiple sequence keys.
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "SEQ-1", Project: "SEQ", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "SEQ-1.1", ParentID: "SEQ-1", Project: "SEQ", Depth: 1, Seq: 1,
		Title: "Child 1", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "SEQ-1.2", ParentID: "SEQ-1", Project: "SEQ", Depth: 1, Seq: 2,
		Title: "Child 2", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h3",
		CreatedAt: now, UpdatedAt: now,
	}))

	exportData, err := srcStore.Export(ctx, "SEQ", "0.1.0")
	require.NoError(t, err)

	destStore := newTestStore(t)
	result, err := destStore.Import(ctx, exportData, sqlite.ImportModeReplace)
	require.NoError(t, err)
	assert.Equal(t, 3, result.NodesCreated)
	assert.True(t, result.FTSRebuilt)

	// Verify sequences were rebuilt: next seq for root should be > 1.
	seq, err := destStore.NextSequence(ctx, "SEQ:")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, seq, 2)

	// Verify sequences for children.
	seq, err = destStore.NextSequence(ctx, "SEQ:SEQ-1")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, seq, 3)
}

// =============================================================================
// Dependency: detectCycle, autoBlock, autoUnblock
// =============================================================================

// TestDetectCycle_ChainedBlocks_DetectsCycle verifies cycle detection through chain.
func TestDetectCycle_ChainedBlocks_DetectsCycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create A, B, C nodes.
	for _, id := range []string{"CY-1", "CY-2", "CY-3"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "CY", Depth: 0, Seq: 1, Title: "Node " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// A blocks B, B blocks C.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CY-1", ToID: "CY-2", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CY-2", ToID: "CY-3", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))

	// C blocks A should create a cycle: C -> A -> B -> C.
	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "CY-3", ToID: "CY-1", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	})
	assert.ErrorIs(t, err, model.ErrCycleDetected)
}

// TestDetectCycle_NoCycle_Succeeds verifies non-cyclic dependencies work.
func TestDetectCycle_NoCycle_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, id := range []string{"NC-1", "NC-2", "NC-3"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "NC", Depth: 0, Seq: 1, Title: "Node " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// A blocks B, A blocks C — no cycle.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "NC-1", ToID: "NC-2", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))
	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "NC-1", ToID: "NC-3", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	})
	assert.NoError(t, err)
}

// TestAddDependency_Duplicate_ReturnsAlreadyExists verifies duplicate rejection.
func TestAddDependency_Duplicate_ReturnsAlreadyExists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, id := range []string{"DUP-1", "DUP-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "DUP", Depth: 0, Seq: 1, Title: "Node",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	dep := &model.Dependency{
		FromID: "DUP-1", ToID: "DUP-2", DepType: model.DepTypeRelated,
		CreatedAt: now, CreatedBy: "pm",
	}
	require.NoError(t, s.AddDependency(ctx, dep))

	err := s.AddDependency(ctx, dep)
	assert.ErrorIs(t, err, model.ErrAlreadyExists)
}

// TestRemoveDependency_MissingDep_ReturnsNotFound verifies missing dep handling.
func TestRemoveDependency_MissingDep_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.RemoveDependency(ctx, "NOPE-1", "NOPE-2", model.DepTypeBlocks)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestRemoveDependency_BlocksType_AutoUnblocks verifies auto-unblock on removal.
func TestRemoveDependency_BlocksType_AutoUnblocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, id := range []string{"AU-1", "AU-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "AU", Depth: 0, Seq: 1, Title: "Node",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// Add blocks dep: AU-1 blocks AU-2. This should auto-block AU-2.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "AU-1", ToID: "AU-2", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))

	// AU-2 should be blocked.
	node, err := s.GetNode(ctx, "AU-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, node.Status)

	// Remove the block dep.
	require.NoError(t, s.RemoveDependency(ctx, "AU-1", "AU-2", model.DepTypeBlocks))

	// AU-2 should be auto-unblocked (restored to open).
	node, err = s.GetNode(ctx, "AU-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestAutoBlock_InProgressNode_GetsBlocked verifies in_progress nodes are blocked.
func TestAutoBlock_InProgressNode_GetsBlocked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create two nodes, set target to in_progress via claim.
	for _, id := range []string{"AB-1", "AB-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "AB", Depth: 0, Seq: 1, Title: "Node",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// Claim AB-2 to make it in_progress.
	require.NoError(t, s.ClaimNode(ctx, "AB-2", "agent-1"))

	// AB-1 blocks AB-2 should auto-block it.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "AB-1", ToID: "AB-2", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))

	node, err := s.GetNode(ctx, "AB-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, node.Status)
}

// TestAutoUnblock_TwoBlockers_StaysBlockedUntilBothRemoved verifies with remaining blockers.
func TestAutoUnblock_TwoBlockers_StaysBlockedUntilBothRemoved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, id := range []string{"MB-1", "MB-2", "MB-3"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "MB", Depth: 0, Seq: 1, Title: "Node",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// MB-1 blocks MB-3 and MB-2 blocks MB-3.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "MB-1", ToID: "MB-3", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "MB-2", ToID: "MB-3", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))

	// MB-3 should be blocked.
	node, err := s.GetNode(ctx, "MB-3")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, node.Status)

	// Remove one blocker.
	require.NoError(t, s.RemoveDependency(ctx, "MB-1", "MB-3", model.DepTypeBlocks))

	// MB-3 should still be blocked (MB-2 still blocks it).
	node, err = s.GetNode(ctx, "MB-3")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, node.Status)

	// Remove second blocker.
	require.NoError(t, s.RemoveDependency(ctx, "MB-2", "MB-3", model.DepTypeBlocks))

	// Now MB-3 should be unblocked.
	node, err = s.GetNode(ctx, "MB-3")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestGetBlockers_WithResolvedAndUnresolved verifies blocker filtering.
func TestGetBlockers_WithResolvedAndUnresolved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, id := range []string{"GB-1", "GB-2", "GB-3"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "GB", Depth: 0, Seq: 1, Title: "Node",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	// GB-1 blocks GB-3 and GB-2 blocks GB-3.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "GB-1", ToID: "GB-3", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "GB-2", ToID: "GB-3", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))

	// Transition GB-1 to done — it becomes resolved.
	require.NoError(t, s.TransitionStatus(ctx, "GB-1", model.StatusInProgress, "claiming", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "GB-1", model.StatusDone, "done", "agent"))

	// Only GB-2 should be an unresolved blocker now.
	blockers, err := s.GetBlockers(ctx, "GB-3")
	require.NoError(t, err)
	require.Len(t, blockers, 1)
	assert.Equal(t, "GB-2", blockers[0].FromID)
}

// TestGetBlockers_NodeWithoutBlockers_ReturnsEmpty verifies empty blockers.
func TestGetBlockers_NodeWithoutBlockers_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("NB-1", "NB", "No blockers", now)))

	blockers, err := s.GetBlockers(ctx, "NB-1")
	require.NoError(t, err)
	assert.Empty(t, blockers)
}

// =============================================================================
// Cancel: cascade cancel with multi-level tree
// =============================================================================

// TestCancelNode_Cascade_CancelsAllDescendants verifies cascade cancel per FR-6.3.
func TestCancelNode_Cascade_CancelsAllDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Build tree: CC-1 -> CC-1.1 -> CC-1.1.1, CC-1 -> CC-1.2.
	require.NoError(t, s.CreateNode(ctx, makeRootNode("CX-1", "CX", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("CX-1.1", "CX-1", "CX", "Child 1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("CX-1.2", "CX-1", "CX", "Child 2", 1, 2, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("CX-1.1.1", "CX-1.1", "CX", "Grandchild", 2, 1, now)))

	err := s.CancelNode(ctx, "CX-1", "project cancelled", "pm", true)
	require.NoError(t, err)

	// All descendants should be cancelled.
	for _, id := range []string{"CX-1.1", "CX-1.2", "CX-1.1.1"} {
		var status string
		err := s.QueryRow(ctx, "SELECT status FROM nodes WHERE id = ?", id).Scan(&status)
		require.NoError(t, err)
		assert.Equal(t, "cancelled", status, "node %s should be cancelled", id)
	}
}

// TestCancelNode_NoCascade_DescendantsUnchanged verifies non-cascade cancel.
func TestCancelNode_NoCascade_DescendantsUnchanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CN-1", "CN", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("CN-1.1", "CN-1", "CN", "Child", 1, 1, now)))

	err := s.CancelNode(ctx, "CN-1", "not needed", "pm", false)
	require.NoError(t, err)

	// Child should still be open.
	node, err := s.GetNode(ctx, "CN-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestCancelNode_EmptyReason_ReturnsError verifies reason validation.
func TestCancelNode_EmptyReason_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CR-1", "CR", "Root", now)))

	err := s.CancelNode(ctx, "CR-1", "", "pm", false)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCancelNode_DoneNode_ReturnsInvalidTransition verifies done->cancelled rejection.
func TestCancelNode_DoneNode_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("DC-1", "DC", "Root", now)))
	require.NoError(t, s.TransitionStatus(ctx, "DC-1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "DC-1", model.StatusDone, "complete", "agent"))

	err := s.CancelNode(ctx, "DC-1", "should fail", "pm", false)
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestCancelNode_NonExistent_ReturnsNotFound verifies missing node.
func TestCancelNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.CancelNode(ctx, "NOPE-1", "reason", "pm", false)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestCancelNode_RecalculatesParentProgress verifies parent progress per FR-5.4.
func TestCancelNode_RecalculatesParentProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CP-1", "CP", "Parent", now)))

	child1 := makeChildNode("CP-1.1", "CP-1", "CP", "Done Child", 1, 1, now)
	child1.Status = model.StatusDone
	child1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("CP-1.2", "CP-1", "CP", "Open Child", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	// Parent at 0.5.
	parent, err := s.GetNode(ctx, "CP-1")
	require.NoError(t, err)
	assert.InDelta(t, 0.5, parent.Progress, 0.01)

	// Cancel child2 — parent should go to 1.0 (only done child counted).
	require.NoError(t, s.CancelNode(ctx, "CP-1.2", "not needed", "pm", false))

	parent, err = s.GetNode(ctx, "CP-1")
	require.NoError(t, err)
	assert.InDelta(t, 1.0, parent.Progress, 0.01)
}

// =============================================================================
// Transition: activity entries, closed_at, previous_status
// =============================================================================

// TestTransitionStatus_ToDone_SetsClosedAt verifies closed_at on done.
func TestTransitionStatus_ToDone_SetsClosedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TD-1", "TD", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "TD-1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "TD-1", model.StatusDone, "complete", "agent"))

	node, err := s.GetNode(ctx, "TD-1")
	require.NoError(t, err)
	assert.NotNil(t, node.ClosedAt)
	assert.Equal(t, model.StatusDone, node.Status)
	assert.InDelta(t, 1.0, node.Progress, 0.001, "done sets progress to 1.0")
}

// TestTransitionStatus_ToCancelled_SetsClosedAt verifies closed_at on cancelled.
func TestTransitionStatus_ToCancelled_SetsClosedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TC-1", "TC", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "TC-1", model.StatusCancelled, "not needed", "pm"))

	node, err := s.GetNode(ctx, "TC-1")
	require.NoError(t, err)
	assert.NotNil(t, node.ClosedAt)
	assert.Equal(t, model.StatusCancelled, node.Status)
}

// TestTransitionStatus_Reopen_ClearsClosedAt verifies closed_at cleared on reopen.
func TestTransitionStatus_Reopen_ClearsClosedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("RO-1", "RO", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "RO-1", model.StatusCancelled, "cancel", "pm"))

	// Reopen.
	require.NoError(t, s.TransitionStatus(ctx, "RO-1", model.StatusOpen, "reopen", "pm"))

	node, err := s.GetNode(ctx, "RO-1")
	require.NoError(t, err)
	assert.Nil(t, node.ClosedAt)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestTransitionStatus_ToBlocked_SavesPreviousStatus verifies previous_status.
func TestTransitionStatus_ToBlocked_SavesPreviousStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("PS-1", "PS", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "PS-1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "PS-1", model.StatusBlocked, "waiting", "agent"))

	// Read previous_status from DB directly.
	var prevStatus string
	err := s.QueryRow(ctx, "SELECT previous_status FROM nodes WHERE id = ?", "PS-1").Scan(&prevStatus)
	require.NoError(t, err)
	assert.Equal(t, "in_progress", prevStatus)
}

// TestTransitionStatus_Idempotent_SameStatus_NoError verifies FR-7.7a.
func TestTransitionStatus_Idempotent_SameStatus_NoError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("ID-1", "ID", "Node", now)))

	// Transition to same status — should be idempotent.
	err := s.TransitionStatus(ctx, "ID-1", model.StatusOpen, "no change", "agent")
	assert.NoError(t, err)
}

// TestTransitionStatus_InvalidTransition_ReturnsError verifies invalid transitions.
func TestTransitionStatus_InvalidTransition_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("IT-1", "IT", "Node", now)))

	// open -> done is invalid (must go through in_progress first).
	err := s.TransitionStatus(ctx, "IT-1", model.StatusDone, "skip ahead", "agent")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestTransitionStatus_NonExistent_ReturnsNotFound verifies missing node.
func TestTransitionStatus_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.TransitionStatus(ctx, "NOPE-1", model.StatusOpen, "reason", "agent")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestTransitionStatus_RecordsActivity verifies activity entry.
func TestTransitionStatus_RecordsActivity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("ACT-1", "ACT", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "ACT-1", model.StatusInProgress, "starting work", "agent-1"))

	var activityJSON string
	err := s.QueryRow(ctx, "SELECT activity FROM nodes WHERE id = ?", "ACT-1").Scan(&activityJSON)
	require.NoError(t, err)

	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(activityJSON), &entries))
	assert.GreaterOrEqual(t, len(entries), 2, "should have created + status_change entries")
}

// TestTransitionStatus_RecalculatesParentProgress verifies parent progress update.
func TestTransitionStatus_RecalculatesParentProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TP-1", "TP", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TP-1.1", "TP-1", "TP", "Child", 1, 1, now)))

	// Transition child to done.
	require.NoError(t, s.TransitionStatus(ctx, "TP-1.1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "TP-1.1", model.StatusDone, "complete", "agent"))

	parent, err := s.GetNode(ctx, "TP-1")
	require.NoError(t, err)
	assert.InDelta(t, 1.0, parent.Progress, 0.01)
}

// =============================================================================
// Claim / Unclaim / ForceReclaim
// =============================================================================

// TestClaimNode_OpenNode_SetsInProgress verifies basic claim.
func TestClaimNode_OpenNode_SetsInProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-1", "CL", "Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "CL-1", "agent-1"))

	node, err := s.GetNode(ctx, "CL-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, node.Status)
	assert.Equal(t, "agent-1", node.Assignee)
	assert.Equal(t, model.AgentStateWorking, node.AgentState)
}

// TestClaimNode_AlreadyClaimed_ReturnsError verifies double claim rejection.
func TestClaimNode_AlreadyClaimed_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-2", "CL", "Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "CL-2", "agent-1"))

	err := s.ClaimNode(ctx, "CL-2", "agent-2")
	assert.ErrorIs(t, err, model.ErrAlreadyClaimed)
}

// TestClaimNode_BlockedNode_ReturnsError verifies blocked claim rejection.
func TestClaimNode_BlockedNode_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-3", "CL", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "CL-3", model.StatusBlocked, "waiting", "pm"))

	err := s.ClaimNode(ctx, "CL-3", "agent-1")
	assert.ErrorIs(t, err, model.ErrNodeBlocked)
}

// TestClaimNode_DoneNode_ReturnsInvalidTransition verifies terminal state claim.
func TestClaimNode_DoneNode_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-4", "CL", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "CL-4", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "CL-4", model.StatusDone, "complete", "agent"))

	err := s.ClaimNode(ctx, "CL-4", "agent-2")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestClaimNode_DeferredPast_Succeeds verifies claiming deferred node past due.
func TestClaimNode_DeferredPast_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-5", "CL", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "CL-5", model.StatusDeferred, "wait", "pm"))

	// Set defer_until to the past.
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	_, err := s.WriteDB().ExecContext(ctx,
		"UPDATE nodes SET defer_until = ? WHERE id = ?", past, "CL-5")
	require.NoError(t, err)

	err = s.ClaimNode(ctx, "CL-5", "agent-1")
	assert.NoError(t, err)
}

// TestClaimNode_DeferredFuture_ReturnsStillDeferred verifies future defer rejection.
func TestClaimNode_DeferredFuture_ReturnsStillDeferred(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("CL-6", "CL", "Node", now)))
	require.NoError(t, s.TransitionStatus(ctx, "CL-6", model.StatusDeferred, "wait", "pm"))

	// Set defer_until to the future.
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	_, err := s.WriteDB().ExecContext(ctx,
		"UPDATE nodes SET defer_until = ? WHERE id = ?", future, "CL-6")
	require.NoError(t, err)

	err = s.ClaimNode(ctx, "CL-6", "agent-1")
	assert.ErrorIs(t, err, model.ErrStillDeferred)
}

// TestClaimNode_NonExistent_ReturnsNotFound verifies missing node.
func TestClaimNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.ClaimNode(ctx, "NOPE-1", "agent-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUnclaimNode_InProgress_RestoresOpen verifies basic unclaim.
func TestUnclaimNode_InProgress_RestoresOpen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UC-1", "UC", "Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "UC-1", "agent-1"))
	require.NoError(t, s.UnclaimNode(ctx, "UC-1", "taking a break", "agent-1"))

	node, err := s.GetNode(ctx, "UC-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
	assert.Empty(t, node.Assignee)
}

// TestUnclaimNode_EmptyReason_ReturnsError verifies reason validation.
func TestUnclaimNode_EmptyReason_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UnclaimNode(ctx, "UC-2", "", "agent")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestUnclaimNode_NotInProgress_ReturnsInvalidTransition verifies wrong state.
func TestUnclaimNode_NotInProgress_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UC-3", "UC", "Node", now)))

	err := s.UnclaimNode(ctx, "UC-3", "reason", "agent")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestUnclaimNode_NonExistent_ReturnsNotFound verifies missing node.
func TestUnclaimNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UnclaimNode(ctx, "NOPE-1", "reason", "agent")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestForceReclaimNode_StaleAgent_Succeeds verifies reclaim from stale agent.
func TestForceReclaimNode_StaleAgent_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("FR-1", "FR", "Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "FR-1", "stale-agent"))

	// Backdate the agent's heartbeat to simulate staleness.
	oldTime := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		oldTime, "stale-agent",
	)
	require.NoError(t, err)

	// Force reclaim with 1-hour threshold.
	err = s.ForceReclaimNode(ctx, "FR-1", "new-agent", 1*time.Hour)
	assert.NoError(t, err)

	node, err := s.GetNode(ctx, "FR-1")
	require.NoError(t, err)
	assert.Equal(t, "new-agent", node.Assignee)
}

// TestForceReclaimNode_ActiveAgent_ReturnsError verifies active agent rejection.
func TestForceReclaimNode_ActiveAgent_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("FR-2", "FR", "Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "FR-2", "active-agent"))

	// ClaimNode auto-registers the agent; update heartbeat to recent time.
	recentTime := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		recentTime, "active-agent",
	)
	require.NoError(t, err)

	// Force reclaim with 1-hour threshold — should fail.
	err = s.ForceReclaimNode(ctx, "FR-2", "new-agent", 1*time.Hour)
	assert.ErrorIs(t, err, model.ErrAgentStillActive)
}

// TestForceReclaimNode_NotInProgress_ReturnsError verifies state check.
func TestForceReclaimNode_NotInProgress_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("FR-3", "FR", "Node", now)))

	err := s.ForceReclaimNode(ctx, "FR-3", "agent", 1*time.Hour)
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestForceReclaimNode_NoHeartbeat_TreatsAsStale verifies null heartbeat treated as stale.
func TestForceReclaimNode_NoHeartbeat_TreatsAsStale(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("FR-4", "FR", "Node", now)))
	require.NoError(t, s.ClaimNode(ctx, "FR-4", "ghost-agent"))

	// ClaimNode auto-registers; clear heartbeat to simulate stale/unknown state.
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE agents SET last_heartbeat = NULL WHERE agent_id = ?`,
		"ghost-agent",
	)
	require.NoError(t, err)

	// Null heartbeat — should treat as stale.
	err = s.ForceReclaimNode(ctx, "FR-4", "new-agent", 1*time.Hour)
	assert.NoError(t, err)
}

// =============================================================================
// Context: GetSiblings, SetAnnotations
// =============================================================================

// TestGetSiblings_RootNode_ReturnsEmpty verifies root has no siblings.
func TestGetSiblings_RootNode_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SB-1", "SB", "Root", now)))

	siblings, err := s.GetSiblings(ctx, "SB-1")
	require.NoError(t, err)
	assert.Empty(t, siblings)
}

// TestGetSiblings_WithSiblings_ReturnsSiblings verifies sibling retrieval.
func TestGetSiblings_WithSiblings_ReturnsSiblings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SB-1", "SB", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SB-1.1", "SB-1", "SB", "Child 1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SB-1.2", "SB-1", "SB", "Child 2", 1, 2, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SB-1.3", "SB-1", "SB", "Child 3", 1, 3, now)))

	siblings, err := s.GetSiblings(ctx, "SB-1.2")
	require.NoError(t, err)
	require.Len(t, siblings, 2)
	assert.Equal(t, "SB-1.1", siblings[0].ID)
	assert.Equal(t, "SB-1.3", siblings[1].ID)
}

// TestGetSiblings_NonExistent_ReturnsNotFound verifies missing node.
func TestGetSiblings_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetSiblings(ctx, "NOPE-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestSetAnnotations_EmptyAnnotations_ClearsAnnotations verifies clearing.
func TestSetAnnotations_EmptyAnnotations_ClearsAnnotations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("AN-1", "AN", "Node", now)))

	// Set some annotations.
	require.NoError(t, s.SetAnnotations(ctx, "AN-1", []model.Annotation{
		{ID: "annot-1", Author: "pm", Text: "important note", CreatedAt: now},
	}))

	// Clear them.
	require.NoError(t, s.SetAnnotations(ctx, "AN-1", nil))

	node, err := s.GetNode(ctx, "AN-1")
	require.NoError(t, err)
	assert.Empty(t, node.Annotations)
}

// TestSetAnnotations_MultipleAnnotations_Persists verifies annotation storage.
func TestSetAnnotations_MultipleAnnotations_Persists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("AN-2", "AN", "Node", now)))

	annotations := []model.Annotation{
		{ID: "annot-1", Author: "pm", Text: "first note", CreatedAt: now},
		{ID: "annot-2", Author: "dev", Text: "urgent tag", CreatedAt: now},
	}
	require.NoError(t, s.SetAnnotations(ctx, "AN-2", annotations))

	node, err := s.GetNode(ctx, "AN-2")
	require.NoError(t, err)
	assert.Len(t, node.Annotations, 2)
}

// =============================================================================
// ListNodes: additional filter coverage
// =============================================================================

// TestListNodes_StatusFilter_FiltersCorrectly verifies status filtering.
func TestListNodes_StatusFilter_FiltersCorrectly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("LN-1", "LN", "Open Node", now)))

	n2 := makeRootNode("LN-2", "LN", "Done Node", now)
	n2.Status = model.StatusDone
	n2.Progress = 1.0
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	// Filter for open only.
	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Status: []model.Status{model.StatusOpen},
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, nodes, 1)
	assert.Equal(t, "LN-1", nodes[0].ID)
}

// TestListNodes_UnderFilter_FiltersSubtree verifies subtree filtering.
func TestListNodes_UnderFilter_FiltersSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("LN-1", "LN", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("LN-1.1", "LN-1", "LN", "Child", 1, 1, now)))

	n3 := makeRootNode("LN-2", "LN", "Other Root", now)
	n3.Seq = 2
	n3.ContentHash = "h3"
	require.NoError(t, s.CreateNode(ctx, n3))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Under: "LN-1",
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "LN-1 + LN-1.1")
	assert.Len(t, nodes, 2)
}

// TestListNodes_AssigneeFilter verifies assignee filtering.
func TestListNodes_AssigneeFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("AF-1", "AF", "Assigned", now)
	n1.Assignee = "agent-1"
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("AF-2", "AF", "Unassigned", now)
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Assignee: "agent-1",
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, nodes, 1)
	assert.Equal(t, "AF-1", nodes[0].ID)
}

// TestListNodes_LimitZero_ReturnsCountWithoutData verifies limit=0 returns count.
func TestListNodes_LimitZero_ReturnsCountWithoutData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("ZL-1", "ZL", "Node 1", now)))
	n2 := makeRootNode("ZL-2", "ZL", "Node 2", now)
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 0})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Nil(t, nodes)
}

// TestListNodes_NodeTypeFilter verifies node type filtering.
func TestListNodes_NodeTypeFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("NT-1", "NT", "Story", now)
	n1.NodeType = model.NodeTypeStory
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("NT-2", "NT", "Issue", now)
	n2.NodeType = model.NodeTypeIssue
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		NodeType: "issue",
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "NT-2", nodes[0].ID)
}

// TestListNodes_PriorityFilter verifies priority filtering.
func TestListNodes_PriorityFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("PF-1", "PF", "High", now)
	n1.Priority = model.PriorityHigh
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("PF-2", "PF", "Low", now)
	n2.Priority = model.PriorityLow
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	p := int(model.PriorityHigh)
	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Priority: &p,
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "PF-1", nodes[0].ID)
}

// TestListNodes_LabelsFilter verifies label filtering.
func TestListNodes_LabelsFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeRootNode("LF-1", "LF", "Tagged", now)
	n1.Labels = []string{"urgent", "backend"}
	n1.ContentHash = model.ComputeContentHash(n1.Title, "", "", "", n1.Labels)
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("LF-2", "LF", "Untagged", now)
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Labels: []string{"urgent"},
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "LF-1", nodes[0].ID)
}

// =============================================================================
// Search: additional filter coverage
// =============================================================================

// TestSearchNodes_StatusFilter_FiltersResults verifies search with status filter.
func TestSearchNodes_StatusFilter_FiltersResults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SF-1", Project: "SF", Depth: 0, Seq: 1, Title: "Searchable Open",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SF-2", Project: "SF", Depth: 0, Seq: 2, Title: "Searchable Done",
		Status: model.StatusDone, Priority: model.PriorityMedium, Weight: 1.0,
		Progress: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	nodes, total, err := s.SearchNodes(ctx, "Searchable",
		store.NodeFilter{Status: []model.Status{model.StatusOpen}},
		store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "SF-1", nodes[0].ID)
}

// TestSearchNodes_BlankQuery_ReturnsError verifies empty query rejection.
func TestSearchNodes_BlankQuery_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _, err := s.SearchNodes(ctx, "", store.NodeFilter{}, store.ListOptions{Limit: 10})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestSearchNodes_NoResults_ReturnsEmpty verifies no match handling.
func TestSearchNodes_NoResults_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	nodes, total, err := s.SearchNodes(ctx, "nonexistent_term_xyz",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Nil(t, nodes)
}

// TestSearchNodes_UnderFilter_FiltersSubtree verifies subtree search filter.
func TestSearchNodes_UnderFilter_FiltersSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SU-1", "SU", "Parent node", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SU-1.1", "SU-1", "SU", "Child node", 1, 1, now)))

	n2 := makeRootNode("SU-2", "SU", "Other parent node", now)
	n2.Seq = 2
	n2.ContentHash = "h2"
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.SearchNodes(ctx, "node",
		store.NodeFilter{Under: "SU-1"},
		store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "SU-1 + SU-1.1")
	assert.Len(t, nodes, 2)
}

// =============================================================================
// Node Update coverage
// =============================================================================

// TestUpdateNode_MultipleFields_UpdatesAll verifies multi-field update.
func TestUpdateNode_MultipleFields_UpdatesAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UP-1", "UP", "Original", now)))

	newTitle := "Updated Title"
	newDesc := "Updated Description"
	newPrompt := "Updated Prompt"
	newAcceptance := "Updated Acceptance"
	newAssignee := "agent-2"
	newPriority := model.PriorityHigh
	newStatus := model.StatusInProgress
	newState := model.AgentStateWorking

	err := s.UpdateNode(ctx, "UP-1", &store.NodeUpdate{
		Title:       &newTitle,
		Description: &newDesc,
		Prompt:      &newPrompt,
		Acceptance:  &newAcceptance,
		Priority:    &newPriority,
		Status:      &newStatus,
		Assignee:    &newAssignee,
		AgentState:  &newState,
		Labels:      []string{"new-label"},
	})
	require.NoError(t, err)

	node, err := s.GetNode(ctx, "UP-1")
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", node.Title)
	assert.Equal(t, "Updated Description", node.Description)
	assert.Equal(t, "Updated Prompt", node.Prompt)
	assert.Equal(t, "Updated Acceptance", node.Acceptance)
	assert.Equal(t, model.PriorityHigh, node.Priority)
	assert.Equal(t, "agent-2", node.Assignee)
	assert.Equal(t, []string{"new-label"}, node.Labels)
	assert.NotEmpty(t, node.ContentHash, "content hash should be recomputed")
}

// TestUpdateNode_NoFields_NoOp verifies empty update does nothing.
func TestUpdateNode_NoFields_NoOp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UP-2", "UP", "No Change", now)))

	err := s.UpdateNode(ctx, "UP-2", &store.NodeUpdate{})
	assert.NoError(t, err)
}

// TestUpdateNode_MissingNode_ReturnsNotFound verifies missing node.
func TestUpdateNode_MissingNode_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	title := "New Title"
	err := s.UpdateNode(ctx, "NOPE-1", &store.NodeUpdate{Title: &title})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUpdateNode_DeletedNode_ReturnsNotFound verifies deleted node update rejection.
func TestUpdateNode_DeletedNode_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("UP-3", "UP", "To Delete", now)))
	require.NoError(t, s.DeleteNode(ctx, "UP-3", false, "admin"))

	title := "New Title"
	err := s.UpdateNode(ctx, "UP-3", &store.NodeUpdate{Title: &title})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// =============================================================================
// Stats: scoped progress
// =============================================================================

// TestGetStats_ScopedProgress_WeightedAverage verifies scoped progress calc.
func TestGetStats_ScopedProgress_WeightedAverage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SP-1", Project: "SP", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 2.0,
		Progress: 0.0, NodeType: model.NodeTypeStory, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SP-1.1", ParentID: "SP-1", Project: "SP", Depth: 1, Seq: 1,
		Title: "Done Child", Status: model.StatusDone, Priority: model.PriorityMedium,
		Weight: 1.0, Progress: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now, UpdatedAt: now,
	}))

	stats, err := s.GetStats(ctx, "SP-1")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalNodes)
	assert.True(t, stats.Progress > 0, "scoped progress should be non-zero")
}

// =============================================================================
// Tree: GetTree coverage
// =============================================================================

// TestGetTree_EmptyRootID_ReturnsError verifies empty root ID.
func TestGetTree_EmptyRootID_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetTree(ctx, "", 5)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestGetTree_MaxDepthZero_ReturnsOnlyRoot verifies depth=0 returns root only.
func TestGetTree_MaxDepthZero_ReturnsOnlyRoot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TR-1", "TR", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1", "TR-1", "TR", "Child", 1, 1, now)))

	nodes, err := s.GetTree(ctx, "TR-1", 0)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "TR-1", nodes[0].ID)
}

// TestGetTree_MultiLevel_RespectsMaxDepth verifies depth limiting.
func TestGetTree_MultiLevel_RespectsMaxDepth(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TR-1", "TR", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1", "TR-1", "TR", "Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1.1", "TR-1.1", "TR", "Grandchild", 2, 1, now)))

	// maxDepth=1 should return root + children only.
	nodes, err := s.GetTree(ctx, "TR-1", 1)
	require.NoError(t, err)
	require.Len(t, nodes, 2)
}

// TestGetTree_NonExistent_ReturnsNotFound verifies missing root.
func TestGetTree_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetTree(ctx, "NOPE-1", 5)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetTree_ExcludesDeleted verifies deleted nodes excluded from tree.
func TestGetTree_ExcludesDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("TR-1", "TR", "Root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.1", "TR-1", "TR", "Active", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("TR-1.2", "TR-1", "TR", "Deleted", 1, 2, now)))

	require.NoError(t, s.DeleteNode(ctx, "TR-1.2", false, "admin"))

	nodes, err := s.GetTree(ctx, "TR-1", 5)
	require.NoError(t, err)
	require.Len(t, nodes, 2) // Root + active child only.
}

// =============================================================================
// Node scan: marshalJSONField edge cases
// =============================================================================

// TestCreateNode_WithMetadata_PersistsJSON verifies metadata JSON round-trip.
func TestCreateNode_WithMetadata_PersistsJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("MD-1", "MD", "Metadata Node", now)
	node.Metadata = json.RawMessage(`{"key":"value","nested":{"a":1}}`)
	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "MD-1")
	require.NoError(t, err)
	assert.NotNil(t, got.Metadata)
	assert.JSONEq(t, `{"key":"value","nested":{"a":1}}`, string(got.Metadata))
}

// TestCreateNode_WithCodeRefs_PersistsJSON verifies code_refs round-trip.
func TestCreateNode_WithCodeRefs_PersistsJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("CR-1", "CR", "CodeRef Node", now)
	node.CodeRefs = []model.CodeRef{{File: "main.go", Line: 42}}
	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "CR-1")
	require.NoError(t, err)
	require.Len(t, got.CodeRefs, 1)
	assert.Equal(t, "main.go", got.CodeRefs[0].File)
	assert.Equal(t, 42, got.CodeRefs[0].Line)
}

// TestCreateNode_WithDeferUntil_PersistsTime verifies defer_until round-trip.
func TestCreateNode_WithDeferUntil_PersistsTime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	deferTime := now.Add(24 * time.Hour)

	node := makeRootNode("DU-1", "DU", "Deferred Node", now)
	node.Status = model.StatusDeferred
	node.DeferUntil = &deferTime
	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "DU-1")
	require.NoError(t, err)
	require.NotNil(t, got.DeferUntil)
	assert.Equal(t, deferTime.UTC(), got.DeferUntil.UTC())
}
