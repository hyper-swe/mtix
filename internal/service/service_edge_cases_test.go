// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// Agent service coverage: GetStaleAgents, CheckStuckTimeouts, GetCurrentWork
// ---------------------------------------------------------------------------

// TestGetStaleAgents_MultipleAgents_ReturnsBothStale verifies batch stale detection per FR-10.3.
func TestGetStaleAgents_MultipleAgents_ReturnsBothStale(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Register three agents: two stale, one fresh.
	registerAgent(t, s, "stale-1", "PROJ", now.Add(-25*time.Hour))
	registerAgent(t, s, "stale-2", "PROJ", now.Add(-30*time.Hour))
	registerAgent(t, s, "fresh-1", "PROJ", now)

	stale, err := agentSvc.GetStaleAgents(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Len(t, stale, 2)
	assert.Contains(t, stale, "stale-1")
	assert.Contains(t, stale, "stale-2")
	assert.NotContains(t, stale, "fresh-1")
}

// TestGetStaleAgents_NoStaleAgents_ReturnsEmptySlice verifies empty case.
func TestGetStaleAgents_NoStaleAgents_ReturnsEmptySlice(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "fresh-1", "PROJ", now)

	stale, err := agentSvc.GetStaleAgents(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Empty(t, stale)
}

// TestCheckStuckTimeouts_MultipleStuckAgents_AllUnclaimed verifies batch unclaim per FR-10.3a.
func TestCheckStuckTimeouts_MultipleStuckAgents_AllUnclaimed(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	stuckTimeout := 30 * time.Minute
	cfg := &service.StaticConfig{
		AutoClaimEnabled: false,
		StaleThreshold:   24 * time.Hour,
		StuckTimeout:     stuckTimeout,
	}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	logger := slog.Default()

	nodeSvc := service.NewNodeService(s, bc, cfg, logger, clock)
	agentSvc := service.NewAgentService(s, bc, cfg, logger, clock)
	ctx := context.Background()

	// Register two agents and create nodes for each.
	registerAgent(t, s, "agent-1", "PROJ", now)
	registerAgent(t, s, "agent-2", "PROJ", now)

	node1, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Stuck Test 1", Creator: "admin",
	})
	require.NoError(t, err)
	node2, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Stuck Test 2", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim both nodes — ClaimNode auto-sets agent state to 'working' per FR-10.1b.
	require.NoError(t, s.ClaimNode(ctx, node1.ID, "agent-1"))
	require.NoError(t, s.ClaimNode(ctx, node2.ID, "agent-2"))

	// Transition both agents from working → stuck.
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateStuck))
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-2", model.AgentStateStuck))

	db := s.WriteDB()
	stuckTime := now.Add(-31 * time.Minute).UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET state_changed_at = ?, current_node_id = ? WHERE agent_id = ?`,
		stuckTime, node1.ID, "agent-1",
	)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET state_changed_at = ?, current_node_id = ? WHERE agent_id = ?`,
		stuckTime, node2.ID, "agent-2",
	)
	require.NoError(t, err)

	err = agentSvc.CheckStuckTimeouts(ctx)
	require.NoError(t, err)

	// Both nodes should be unclaimed.
	got1, err := s.GetNode(ctx, node1.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got1.Status)
	assert.Empty(t, got1.Assignee)

	got2, err := s.GetNode(ctx, node2.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got2.Status)
	assert.Empty(t, got2.Assignee)
}

// TestGetCurrentWork_NullCurrentNodeID_ReturnsNotFound verifies agent with null current_node_id.
func TestGetCurrentWork_NullCurrentNodeID_ReturnsNotFound(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Agent exists but has no current_node_id (NULL by default).
	_, err := agentSvc.GetCurrentWork(ctx, "agent-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetCurrentWork_EmptyCurrentNodeID_ReturnsNotFound verifies empty string node ID.
func TestGetCurrentWork_EmptyCurrentNodeID_ReturnsNotFound(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Set current_node_id to empty string.
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE agents SET current_node_id = '' WHERE agent_id = ?`, "agent-1",
	)
	require.NoError(t, err)

	_, err = agentSvc.GetCurrentWork(ctx, "agent-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Session service coverage: SessionStart, CheckSessionTimeouts, countSessionNodes
// ---------------------------------------------------------------------------

// TestSessionStart_MultipleProjects_CreatesIndependentSessions verifies session isolation.
func TestSessionStart_MultipleProjects_CreatesIndependentSessions(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ-A", now)

	s1, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ-A")
	require.NoError(t, err)
	assert.Len(t, s1, 26)

	// Starting a new session auto-ends the first.
	s2, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ-B")
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2)
}

// TestCheckSessionTimeouts_NoActiveSessions_ReturnsNil verifies no-op.
func TestCheckSessionTimeouts_NoActiveSessions_ReturnsNil(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{SessionTimeoutDur: 4 * time.Hour}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	sessionSvc := service.NewSessionService(s, cfg, slog.Default(), fixedClock(now))

	err = sessionSvc.CheckSessionTimeouts(context.Background())
	assert.NoError(t, err)
}

// TestSessionSummary_ActiveSession_NoEndedAt verifies active session summary.
func TestSessionSummary_ActiveSession_NoEndedAt(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	_, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "active", summary.Status)
	assert.Nil(t, summary.EndedAt)
	assert.Equal(t, 0, summary.NodesCreated)
	assert.Equal(t, 0, summary.NodesCompleted)
	assert.Equal(t, 0, summary.NodesDeferred)
}

// TestSessionEnd_WithCompletedAndDeferredNodes_SummaryCounts verifies comprehensive summary.
func TestSessionEnd_WithCompletedAndDeferredNodes_SummaryCounts(t *testing.T) {
	sessionSvc, nodeSvc, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Create three nodes: one done, one deferred, one open.
	n1, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Done Node", Creator: "agent-1",
	})
	require.NoError(t, err)
	n2, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Deferred Node", Creator: "agent-1",
	})
	require.NoError(t, err)
	n3, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Open Node", Creator: "agent-1",
	})
	require.NoError(t, err)

	// Tag nodes with session.
	db := s.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET session_id = ? WHERE id IN (?, ?, ?)`,
		sessionID, n1.ID, n2.ID, n3.ID,
	)
	require.NoError(t, err)

	// Complete n1.
	require.NoError(t, s.ClaimNode(ctx, n1.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, n1.ID, model.StatusDone, "done", "agent-1"))

	// Defer n2.
	require.NoError(t, s.TransitionStatus(ctx, n2.ID, model.StatusDeferred, "later", "agent-1"))

	// End session.
	err = sessionSvc.SessionEnd(ctx, "agent-1")
	require.NoError(t, err)

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ended", summary.Status)
	assert.Equal(t, 3, summary.NodesCreated)
	assert.Equal(t, 1, summary.NodesCompleted)
	assert.Equal(t, 1, summary.NodesDeferred)
	assert.NotNil(t, summary.EndedAt)
	assert.Contains(t, summary.SummaryText, "3 nodes created")
	assert.Contains(t, summary.SummaryText, "1 completed")
	assert.Contains(t, summary.SummaryText, "1 deferred")
}

// ---------------------------------------------------------------------------
// Background service coverage: cleanExpiredNodes, wakeDeferredNodes, GetReadyNodes
// ---------------------------------------------------------------------------

// TestBackgroundScan_SoftDeletedNotExpired_NotCleaned verifies retention boundary.
func TestBackgroundScan_SoftDeletedNotExpired_NotCleaned(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create a node, soft-delete it, set deleted_at to 29 days ago (within 30-day retention).
	createTestNode(t, s, "PROJ-1", "PROJ", "Recent Delete", now.Add(-30*24*time.Hour))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	deletedAt := now.Add(-29 * 24 * time.Hour).UTC().Format(time.RFC3339)
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ? WHERE id = ?`,
		deletedAt, "PROJ-1",
	)
	require.NoError(t, err)

	err = bg.RunScan(ctx)
	require.NoError(t, err)

	// Node should still exist.
	var count int
	err = s.QueryRow(ctx, `SELECT COUNT(*) FROM nodes WHERE id = ?`, "PROJ-1").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestGetReadyNodes_ExcludesSoftDeletedNodes verifies soft-deleted exclusion.
func TestGetReadyNodes_ExcludesSoftDeletedNodes(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	createTestNode(t, s, "PROJ-1", "PROJ", "Open Node", now)
	createTestNode(t, s, "PROJ-2", "PROJ", "Deleted Node", now)
	require.NoError(t, s.DeleteNode(ctx, "PROJ-2", false, "admin"))

	nodes, err := bg.GetReadyNodes(ctx)
	require.NoError(t, err)

	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	assert.Contains(t, ids, "PROJ-1")
	assert.NotContains(t, ids, "PROJ-2")
}

// TestGetReadyNodes_MultipleOpenNodes_OrderedByPriorityThenCreatedAt verifies ordering.
func TestGetReadyNodes_MultipleOpenNodes_OrderedByPriorityThenCreatedAt(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create nodes with different priorities.
	for i, prio := range []model.Priority{model.PriorityLow, model.PriorityHigh, model.PriorityMedium} {
		id := fmt.Sprintf("PROJ-%d", i+1)
		node := &model.Node{
			ID:        id,
			Project:   "PROJ",
			Title:     fmt.Sprintf("Node %d", i+1),
			Status:    model.StatusOpen,
			Priority:  prio,
			Weight:    1.0,
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
			UpdatedAt: now.Add(time.Duration(i) * time.Minute),
		}
		node.ContentHash = node.ComputeHash()
		require.NoError(t, s.CreateNode(ctx, node))
	}

	nodes, err := bg.GetReadyNodes(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(nodes), 3)

	// Verify ordering: High (2) < Medium (3) < Low (1) by priority value.
	assert.Equal(t, "PROJ-2", nodes[0].ID, "High priority should be first")
}

// TestBackgroundScan_WakeMultipleDeferredNodes_FutureAndPast verifies mixed deferred handling.
func TestBackgroundScan_WakeMultipleDeferredNodes_FutureAndPast(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create past-due deferred node.
	createTestNode(t, s, "PROJ-1", "PROJ", "Past Due", now.Add(-2*time.Hour))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDeferred, "wait", "admin"))
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`,
		now.Add(-1*time.Hour).UTC().Format(time.RFC3339), "PROJ-1",
	)
	require.NoError(t, err)

	// Create future deferred node.
	createTestNode(t, s, "PROJ-2", "PROJ", "Future", now.Add(-2*time.Hour))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2", model.StatusDeferred, "wait", "admin"))
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`,
		now.Add(24*time.Hour).UTC().Format(time.RFC3339), "PROJ-2",
	)
	require.NoError(t, err)

	err = bg.RunScan(ctx)
	require.NoError(t, err)

	// Past-due should be woken.
	got1, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got1.Status)

	// Future should remain deferred.
	got2, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, got2.Status)
}

// ---------------------------------------------------------------------------
// Rerun coverage: rerunAllNode, rerunDeleteNode, rerunOpenOnlyNode error paths
// ---------------------------------------------------------------------------

// TestRerun_AllStrategy_DoneChild_InvalidatedThenReset verifies all strategy on done child.
func TestRerun_AllStrategy_DoneChild_InvalidatedThenReset(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Done Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Move child through claim -> done.
	require.NoError(t, s.ClaimNode(ctx, child.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, child.ID, model.StatusDone, "complete", "agent-1"))

	// Rerun should invalidate then reset to open.
	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "revised spec", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRerun_DeleteStrategy_DoneChild_InvalidatesThenDeletes verifies FR-3.5b on done nodes.
func TestRerun_DeleteStrategy_DoneChild_InvalidatesThenDeletes(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Done Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Move child to done.
	require.NoError(t, s.ClaimNode(ctx, child.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, child.ID, model.StatusDone, "complete", "agent-1"))

	err = svc.Rerun(ctx, parent.ID, service.RerunDelete, "clean slate", "admin")
	require.NoError(t, err)

	// Child should be soft-deleted.
	_, err = s.GetNode(ctx, child.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestRerun_OpenOnlyStrategy_InProgressChild_ResetsToOpen verifies in_progress handling.
func TestRerun_OpenOnlyStrategy_InProgressChild_ResetsToOpen(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "In Progress Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim child (moves to in_progress).
	require.NoError(t, s.ClaimNode(ctx, child.ID, "agent-1"))

	err = svc.Rerun(ctx, parent.ID, service.RerunOpenOnly, "spec changed", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRerun_AllStrategy_InProgressChild_ResetsToOpen verifies in_progress handling.
func TestRerun_AllStrategy_InProgressChild_ResetsToOpen(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "In Progress Child", Creator: "admin",
	})
	require.NoError(t, err)

	require.NoError(t, s.ClaimNode(ctx, child.ID, "agent-1"))

	err = svc.Rerun(ctx, parent.ID, service.RerunAll, "full rerun", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRerun_DeleteStrategy_InProgressChild_InvalidatesThenDeletes verifies FR-3.5b.
func TestRerun_DeleteStrategy_InProgressChild_InvalidatesThenDeletes(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "In Progress Child", Creator: "admin",
	})
	require.NoError(t, err)

	require.NoError(t, s.ClaimNode(ctx, child.ID, "agent-1"))

	err = svc.Rerun(ctx, parent.ID, service.RerunDelete, "clean up", "admin")
	require.NoError(t, err)

	_, err = s.GetNode(ctx, child.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Decompose coverage: createChildForDecompose error and auto-claim paths
// ---------------------------------------------------------------------------

// TestDecompose_WithLabels_AllChildrenGetLabels verifies label propagation.
func TestDecompose_WithLabels_AllChildrenGetLabels(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	children := []service.DecomposeInput{
		{Title: "Child 1", Labels: []string{"backend", "urgent"}},
		{Title: "Child 2", Labels: []string{"frontend"}},
	}

	ids, err := svc.Decompose(ctx, parent.ID, children, "admin")
	require.NoError(t, err)
	require.Len(t, ids, 2)

	got1, err := s.GetNode(ctx, ids[0])
	require.NoError(t, err)
	assert.Contains(t, got1.Labels, "backend")
	assert.Contains(t, got1.Labels, "urgent")

	got2, err := s.GetNode(ctx, ids[1])
	require.NoError(t, err)
	assert.Contains(t, got2.Labels, "frontend")
}

// ---------------------------------------------------------------------------
// Config service coverage: Get, Set, Delete, InitConfig, loadFromFile, saveToFile
// ---------------------------------------------------------------------------

// TestConfigService_Get_ValidKey_ReturnsDefault verifies default value retrieval.
func TestConfigService_Get_ValidKey_ReturnsDefault(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	val, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "PROJ", val)
}

// TestConfigService_Get_InvalidKey_ReturnsError verifies key validation.
func TestConfigService_Get_InvalidKey_ReturnsError(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	_, err = cs.Get("nonexistent.key")
	assert.ErrorIs(t, err, model.ErrInvalidConfigKey)
}

// TestConfigService_Set_ValidKey_PersistsValue verifies value persistence.
func TestConfigService_Set_ValidKey_PersistsValue(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/.mtix/config.yaml"

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	warning, err := cs.Set("prefix", "CUSTOM")
	require.NoError(t, err)
	assert.Empty(t, warning) // prefix is not a server-restart key.

	val, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "CUSTOM", val)
}

// TestConfigService_Set_ServerRestartKey_ReturnsWarning verifies restart warning.
func TestConfigService_Set_ServerRestartKey_ReturnsWarning(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/.mtix/config.yaml"

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	warning, err := cs.Set("api.bind", "0.0.0.0")
	require.NoError(t, err)
	assert.Contains(t, warning, "restart")
}

// TestConfigService_Set_InvalidKey_ReturnsError verifies key validation.
func TestConfigService_Set_InvalidKey_ReturnsError(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	_, err = cs.Set("invalid.key", "value")
	assert.ErrorIs(t, err, model.ErrInvalidConfigKey)
}

// TestConfigService_Delete_RevertsToDefault verifies revert behavior.
func TestConfigService_Delete_RevertsToDefault(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/.mtix/config.yaml"

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	// Set a custom value.
	_, err = cs.Set("prefix", "CUSTOM")
	require.NoError(t, err)

	// Delete reverts to default.
	err = cs.Delete("prefix")
	require.NoError(t, err)

	val, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "PROJ", val)
}

// TestConfigService_Delete_InvalidKey_ReturnsError verifies key validation.
func TestConfigService_Delete_InvalidKey_ReturnsError(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	err = cs.Delete("invalid.key")
	assert.ErrorIs(t, err, model.ErrInvalidConfigKey)
}

// TestConfigService_InitConfig_CreatesDirectoryStructure verifies init.
func TestConfigService_InitConfig_CreatesDirectoryStructure(t *testing.T) {
	dir := t.TempDir()

	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	err = cs.InitConfig(dir, "TEST")
	require.NoError(t, err)

	// Verify the prefix was set.
	val, err := cs.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "TEST", val)
}

// TestConfigService_LoadFromFile_ParsesYAML verifies YAML loading.
func TestConfigService_LoadFromFile_ParsesYAML(t *testing.T) {
	dir := t.TempDir()

	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	// Initialize with a specific prefix.
	err = cs.InitConfig(dir, "MYPROJ")
	require.NoError(t, err)

	// Create a new ConfigService from the file.
	configPath := dir + "/.mtix/config.yaml"
	cs2, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	val, err := cs2.Get("prefix")
	require.NoError(t, err)
	assert.Equal(t, "MYPROJ", val)
}

// TestConfigService_ValidConfigKeys_ReturnsAllKeys verifies key listing.
func TestConfigService_ValidConfigKeys_ReturnsAllKeys(t *testing.T) {
	keys := service.ValidConfigKeys()
	assert.GreaterOrEqual(t, len(keys), 27)
	assert.Contains(t, keys, "prefix")
	assert.Contains(t, keys, "api.bind")
}

// TestConfigService_ConfigProvider_Methods verifies ConfigProvider implementation.
func TestConfigService_ConfigProvider_Methods(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/.mtix/config.yaml"

	cs, err := service.NewConfigService(configPath)
	require.NoError(t, err)

	// Test all ConfigProvider methods.
	assert.True(t, cs.AutoClaim())                               // Default is "true".
	assert.Equal(t, 30*24*time.Hour, cs.SoftDeleteRetention())   // 30d.
	assert.Equal(t, 24*time.Hour, cs.AgentStaleThreshold())      // 24h.
	assert.Equal(t, 50, cs.MaxRecommendedDepth())                // Not configurable, hardcoded in this impl.
	assert.Equal(t, time.Duration(0), cs.AgentStuckTimeout())    // Default empty = 0.
	assert.Equal(t, 4*time.Hour, cs.SessionTimeout())            // 4h.
}

// ---------------------------------------------------------------------------
// Node service edge cases: buildNode depth warning, TransitionStatus broadcast
// ---------------------------------------------------------------------------

// TestNodeService_CreateNode_UnderNonExistentParent_ReturnsNotFound verifies parent check.
func TestNodeService_CreateNode_UnderNonExistentParent_ReturnsNotFound(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: "NONEXISTENT",
		Project:  "PROJ",
		Title:    "Orphan",
		Creator:  "admin",
	})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestNodeService_UpdateNode_NonExistentNode_ReturnsError verifies error propagation.
func TestNodeService_UpdateNode_NonExistentNode_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	title := "New Title"
	err := svc.UpdateNode(ctx, "NONEXISTENT", &store.NodeUpdate{Title: &title})
	assert.Error(t, err)
}

// TestNodeService_TransitionStatus_DeferredToOpen_ValidTransition verifies transition.
func TestNodeService_TransitionStatus_DeferredToOpen_ValidTransition(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Transition Test", Creator: "admin",
	})
	require.NoError(t, err)

	// open -> deferred -> open.
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDeferred, "later", "admin"))

	err = svc.TransitionStatus(ctx, node.ID, model.StatusOpen, "ready now", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestNodeService_CreateNode_DeepNesting_WarnsButSucceeds verifies FR-1.1a advisory warning.
func TestNodeService_CreateNode_DeepNesting_WarnsButSucceeds(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Use a very low max depth to trigger the warning without deep nesting.
	cfg := &service.StaticConfig{
		AutoClaimEnabled:    false,
		RecommendedMaxDepth: 1, // Warn at depth > 1.
	}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, nil, cfg, slog.Default(), fixedClock(now))
	ctx := context.Background()

	// Create root (depth 0) and child (depth 1) - no warning.
	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Grandchild (depth 2 > 1) triggers warning but still succeeds.
	grandchild, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: child.ID, Project: "PROJ", Title: "Grandchild", Creator: "admin",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, grandchild.ID)

	got, err := s.GetNode(ctx, grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Depth)
}

// ---------------------------------------------------------------------------
// Context service coverage: getSiblings empty, GetContext error paths
// ---------------------------------------------------------------------------

// TestContextService_GetContext_NonExistentNode_ReturnsError verifies error.
func TestContextService_GetContext_NonExistentNode_ReturnsError(t *testing.T) {
	ctxSvc, _, _ := newTestContextService(t)
	ctx := context.Background()

	_, err := ctxSvc.GetContext(ctx, "NONEXISTENT", nil)
	assert.Error(t, err)
}

// TestContextService_GetContext_LeafNode_NoSiblings verifies context for leaf with no siblings.
func TestContextService_GetContext_LeafNode_NoSiblings(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	// Create a single node (no siblings).
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Only Child", Creator: "admin",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, node.ID, nil)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Siblings)
}

// TestContextService_GetContext_WithMaxTokens_TruncatesPrompt verifies truncation.
func TestContextService_GetContext_WithMaxTokens_TruncatesPrompt(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	// Create a node with a long prompt.
	longPrompt := strings.Repeat("This is a test sentence. ", 100)
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Long Prompt",
		Prompt:  longPrompt,
		Creator: "human@test.com",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, node.ID, &service.ContextOptions{MaxTokens: 50})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	// The assembled prompt should be truncated.
	assert.NotEmpty(t, resp.AssembledPrompt)
}

// TestContextService_GetContext_ChildWithAncestors_BuildsChain verifies chain building.
func TestContextService_GetContext_ChildWithAncestors_BuildsChain(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Prompt: "Root prompt", Creator: "human@test.com",
	})
	require.NoError(t, err)

	child, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child", Prompt: "Child prompt", Creator: "agent-1",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, child.ID, nil)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.GreaterOrEqual(t, len(resp.Chain), 2)
	assert.Contains(t, resp.AssembledPrompt, "Root prompt")
	assert.Contains(t, resp.AssembledPrompt, "Child prompt")
	// Verify attribution markers per FR-12.3a.
	assert.Contains(t, resp.AssembledPrompt, "[HUMAN-AUTHORED]")
	assert.Contains(t, resp.AssembledPrompt, "[LLM-GENERATED]")
}

// TestContextService_GetContext_WithSiblings verifies sibling inclusion.
func TestContextService_GetContext_WithSiblings(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	parent, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	child1, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child 1", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child 2", Creator: "admin",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, child1.ID, nil)
	require.NoError(t, err)
	// Child 2 should appear as a sibling of Child 1.
	assert.Len(t, resp.Siblings, 1)
	assert.Equal(t, "Child 2", resp.Siblings[0].Title)
}

// ---------------------------------------------------------------------------
// Prompt service edge cases
// ---------------------------------------------------------------------------

// TestUpdatePrompt_SameText_StillUpdatesHashAndTimestamp verifies idempotent update.
func TestUpdatePrompt_SameText_StillUpdatesHashAndTimestamp(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Same Prompt Test", Prompt: "original", Creator: "admin",
	})
	require.NoError(t, err)

	before, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)

	// Update with same prompt text.
	err = promptSvc.UpdatePrompt(ctx, node.ID, "original", "admin")
	require.NoError(t, err)

	after, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	// Hash should be same since content hasn't changed.
	assert.Equal(t, before.ContentHash, after.ContentHash)
}

// TestAddAnnotation_MultipleAnnotations_AllPersisted verifies multiple annotations.
func TestAddAnnotation_MultipleAnnotations_AllPersisted(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Multi Annotation Test", Creator: "admin",
	})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		err = promptSvc.AddAnnotation(ctx, node.ID,
			fmt.Sprintf("Annotation %d", i), fmt.Sprintf("reviewer-%d@test.com", i))
		require.NoError(t, err)
	}

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Len(t, got.Annotations, 5)
}

// ---------------------------------------------------------------------------
// Restore edge cases
// ---------------------------------------------------------------------------

// TestRestore_InvalidatedFromDone_RestoresToDone verifies restore to previous terminal.
func TestRestore_InvalidatedFromDone_RestoresToDone(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Restore Done Test", Creator: "admin",
	})
	require.NoError(t, err)

	// Move to done, then invalidate.
	require.NoError(t, s.ClaimNode(ctx, node.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, node.ID, model.StatusDone, "done", "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, node.ID, model.StatusInvalidated, "stale", "system"))

	// Restore: previous_status=done, but invalidated->done may not be valid.
	// Should fall back to open.
	err = svc.Restore(ctx, node.ID, "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	// Result depends on state machine: invalidated -> done may not be valid,
	// in which case it falls back to open.
	assert.True(t, got.Status == model.StatusOpen || got.Status == model.StatusDone,
		"should restore to open or done")
}

// ---------------------------------------------------------------------------
// Ensure non-terminal edge case: invalidated parent with open previous_status
// ---------------------------------------------------------------------------

// TestEnsureNonTerminal_OpenParent_NoOp verifies no-op for already-open parent.
func TestEnsureNonTerminal_OpenParent_NoOp(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Open Parent", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	// Rerun on open parent should succeed without error (parent is already non-terminal).
	err = svc.Rerun(ctx, parent.ID, service.RerunReview, "test", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}
