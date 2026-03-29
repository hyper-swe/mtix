// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestNewSessionService_NilConfigAndLogger_UsesDefaults verifies nil fallbacks per FR-10.5a.
func TestNewSessionService_NilConfigAndLogger_UsesDefaults(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewSessionService(s, nil, nil, fixedClock(now))
	require.NotNil(t, svc)

	// Verify it works with defaults.
	ctx := context.Background()
	registerAgent(t, s, "agent-nil", "PROJ", now)
	sessionID, err := svc.SessionStart(ctx, "agent-nil", "PROJ")
	require.NoError(t, err)
	assert.Len(t, sessionID, 26)
}

// TestSessionService_CheckSessionTimeouts_NoTimedOutSessions_ReturnsNil verifies no-op.
func TestSessionService_CheckSessionTimeouts_NoTimedOutSessions_ReturnsNil(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Create a fresh session (within timeout).
	_, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Should not end any sessions.
	err = sessionSvc.CheckSessionTimeouts(ctx)
	assert.NoError(t, err)

	// Session should still be active.
	activeID, err := sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.NotEmpty(t, activeID)
}

// TestSessionService_SessionSummary_WithDeferredNodes verifies deferred count.
func TestSessionService_SessionSummary_WithDeferredNodes(t *testing.T) {
	sessionSvc, nodeSvc, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Create a node and defer it.
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Deferred Node", Creator: "agent-1",
	})
	require.NoError(t, err)

	// Tag with session and defer.
	db := s.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET session_id = ? WHERE id = ?`, sessionID, node.ID,
	)
	require.NoError(t, err)
	require.NoError(t, s.TransitionStatus(ctx, node.ID, model.StatusDeferred, "later", "agent-1"))

	// End session.
	require.NoError(t, sessionSvc.SessionEnd(ctx, "agent-1"))

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 1, summary.NodesCreated)
	assert.Equal(t, 1, summary.NodesDeferred)
	assert.NotNil(t, summary.EndedAt)
}

// TestNewHub_NilLogger_UsesDefault verifies hub nil logger fallback.
func TestNewHub_NilLogger_UsesDefault(t *testing.T) {
	hub := service.NewHub(nil)
	require.NotNil(t, hub)
	assert.Equal(t, 0, hub.SubscriberCount())
}

// TestBroadcaster_Unsubscribe_NonExistentChannel_NoOp verifies safe double-unsub.
func TestBroadcaster_Unsubscribe_NonExistentChannel_NoOp(t *testing.T) {
	hub := service.NewHub(slog.Default())

	ch := hub.Subscribe(service.SubscriptionFilter{})
	hub.Unsubscribe(ch)

	// Second unsubscribe of same channel should be safe.
	hub.Unsubscribe(ch)
	assert.Equal(t, 0, hub.SubscriberCount())
}

// TestNewPromptService_NilBroadcasterAndLogger_UsesDefaults verifies nil fallbacks.
func TestNewPromptService_NilBroadcasterAndLogger_UsesDefaults(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewPromptService(s, nil, nil, fixedClock(now))
	require.NotNil(t, svc)
}

// TestNewContextService_NilConfigAndLogger_UsesDefaults verifies nil fallbacks.
func TestNewContextService_NilConfigAndLogger_UsesDefaults(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	svc := service.NewContextService(s, nil, nil)
	require.NotNil(t, svc)
}

// TestNodeService_CreateNode_AutoClaim_NoParent_NotTriggered verifies no auto-claim for root.
func TestNodeService_CreateNode_AutoClaim_NoParent_NotTriggered(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{AutoClaimEnabled: true}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, nil, cfg, nil, fixedClock(now))
	ctx := context.Background()

	// Root node with auto-claim on — should NOT auto-claim (no parent).
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root Node", Creator: "admin",
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Empty(t, got.Assignee)
}

// TestNodeService_CreateNode_AutoClaim_ParentInProgressNoAssignee_NotTriggered verifies edge case.
func TestNodeService_CreateNode_AutoClaim_ParentInProgressNoAssignee_NotTriggered(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{AutoClaimEnabled: true}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, nil, cfg, nil, fixedClock(now))
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	// Transition parent to in_progress manually (without claiming — no assignee).
	db := s.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET status = 'in_progress' WHERE id = ?`, parent.ID,
	)
	require.NoError(t, err)

	// Create child — should NOT auto-claim (parent has no assignee).
	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child", Creator: "admin",
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Empty(t, got.Assignee)
}

// TestNodeService_CreateNode_AutoClaim_NotTriggered_WhenParentNotInProgress verifies FR-11.2a.
func TestNodeService_CreateNode_AutoClaim_NotTriggered_WhenParentNotInProgress(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{AutoClaimEnabled: true}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, nil, cfg, nil, fixedClock(now))
	ctx := context.Background()

	// Create parent (open, not in_progress).
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Open Parent", Creator: "admin",
	})
	require.NoError(t, err)

	// Create child — should NOT auto-claim because parent is open, not in_progress.
	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID,
		Project:  "PROJ",
		Title:    "Child",
		Creator:  "admin",
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Empty(t, got.Assignee)
}

// errorBroadcaster always returns an error on Broadcast for testing error logging paths.
type errorBroadcaster struct{}

func (e *errorBroadcaster) Broadcast(_ context.Context, _ service.Event) error {
	return fmt.Errorf("broadcast failed")
}

// TestNodeService_BroadcastError_DoesNotFailOperation verifies broadcast errors are swallowed.
func TestNodeService_BroadcastError_DoesNotFailOperation(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, &errorBroadcaster{}, nil, nil, fixedClock(now))
	ctx := context.Background()

	// CreateNode should succeed even if broadcast fails.
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Broadcast Error", Creator: "admin",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, node.ID)

	// UpdateNode should succeed.
	newTitle := "Updated"
	err = svc.UpdateNode(ctx, node.ID, &store.NodeUpdate{Title: &newTitle})
	assert.NoError(t, err)

	// DeleteNode should succeed.
	err = svc.DeleteNode(ctx, node.ID, false, "admin")
	assert.NoError(t, err)
}

// TestAgentService_BroadcastError_DoesNotFailOperation verifies agent broadcast resilience.
func TestAgentService_BroadcastError_DoesNotFailOperation(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewAgentService(s, &errorBroadcaster{}, nil, nil, fixedClock(now))
	ctx := context.Background()

	registerAgent(t, s, "agent-1", "PROJ", now)

	// UpdateAgentState should succeed even if broadcast fails.
	err = svc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking)
	assert.NoError(t, err)

	// Stuck transition should also succeed.
	err = svc.UpdateAgentState(ctx, "agent-1", model.AgentStateStuck)
	assert.NoError(t, err)
}

// TestPromptService_BroadcastError_DoesNotFailOperation verifies prompt broadcast resilience.
func TestPromptService_BroadcastError_DoesNotFailOperation(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	nodeSvc := service.NewNodeService(s, nil, nil, nil, fixedClock(now))
	promptSvc := service.NewPromptService(s, &errorBroadcaster{}, nil, fixedClock(now))
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Prompt Error Test", Creator: "admin",
	})
	require.NoError(t, err)

	// UpdatePrompt should succeed even if broadcast fails.
	err = promptSvc.UpdatePrompt(ctx, node.ID, "New prompt text", "admin")
	assert.NoError(t, err)

	// AddAnnotation should succeed.
	err = promptSvc.AddAnnotation(ctx, node.ID, "An annotation", "reviewer")
	assert.NoError(t, err)
}

// TestNodeService_UndeleteNode_RestoresDeletedNode verifies undelete functionality.
func TestNodeService_UndeleteNode_RestoresDeletedNode(t *testing.T) {
	svc, s, bc := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "To Undelete",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Delete the node.
	require.NoError(t, svc.DeleteNode(ctx, node.ID, false, "admin"))
	bc.Reset()

	// Undelete the node.
	err = svc.UndeleteNode(ctx, node.ID)
	require.NoError(t, err)

	// Verify node is accessible again.
	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "To Undelete", got.Title)
	assert.Nil(t, got.DeletedAt)

	// Verify event was broadcast.
	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventNodeUndeleted, events[0].Type)
	assert.Equal(t, node.ID, events[0].NodeID)
}

// TestNodeService_UndeleteNode_NonExistent_ReturnsError verifies error handling.
func TestNodeService_UndeleteNode_NonExistent_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	err := svc.UndeleteNode(ctx, "NONEXISTENT")
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestAgentService_GetAgentState_ReturnsState verifies state retrieval.
func TestAgentService_GetAgentState_ReturnsState(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Transition to working.
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking))

	// Verify state.
	state, err := agentSvc.GetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateWorking, state)
}

// TestAgentService_GetAgentState_NonExistent_ReturnsNotFound verifies error.
func TestAgentService_GetAgentState_NonExistent_ReturnsNotFound(t *testing.T) {
	agentSvc, _, _, _ := newTestAgentService(t)
	ctx := context.Background()

	_, err := agentSvc.GetAgentState(ctx, "nonexistent-agent")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestAgentService_GetAgentState_MultipleAgents verifies isolation.
func TestAgentService_GetAgentState_MultipleAgents(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Register two agents.
	registerAgent(t, s, "agent-1", "PROJ", now)
	registerAgent(t, s, "agent-2", "PROJ", now)

	// Transition agent-1 to working.
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking))

	// Verify agent-1 is working.
	state1, err := agentSvc.GetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateWorking, state1)

	// Verify agent-2 is still idle.
	state2, err := agentSvc.GetAgentState(ctx, "agent-2")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateIdle, state2)
}

// TestAgentService_GetLastHeartbeat_ReturnsTimestamp verifies heartbeat retrieval.
func TestAgentService_GetLastHeartbeat_ReturnsTimestamp(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Record a heartbeat.
	require.NoError(t, agentSvc.Heartbeat(ctx, "agent-1"))

	// Retrieve heartbeat.
	hb, err := agentSvc.GetLastHeartbeat(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, now.UTC().Format(time.RFC3339), hb.UTC().Format(time.RFC3339))
}

// TestAgentService_GetLastHeartbeat_NonExistent_ReturnsError verifies error.
func TestAgentService_GetLastHeartbeat_NonExistent_ReturnsError(t *testing.T) {
	agentSvc, _, _, _ := newTestAgentService(t)
	ctx := context.Background()

	_, err := agentSvc.GetLastHeartbeat(ctx, "nonexistent-agent")
	require.Error(t, err)
}

// TestAgentService_GetLastHeartbeat_MultipleSessions verifies data isolation.
func TestAgentService_GetLastHeartbeat_MultipleSessions(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Register two agents with different times.
	registerAgent(t, s, "agent-1", "PROJ", now)
	agent2Time := now.Add(-5 * time.Hour)
	registerAgent(t, s, "agent-2", "PROJ", agent2Time)

	// Update heartbeat for agent-1.
	require.NoError(t, agentSvc.Heartbeat(ctx, "agent-1"))

	// Verify agent-1 has new heartbeat.
	hb1, err := agentSvc.GetLastHeartbeat(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, now.UTC().Format(time.RFC3339), hb1.UTC().Format(time.RFC3339))

	// Verify agent-2 still has old heartbeat.
	hb2, err := agentSvc.GetLastHeartbeat(ctx, "agent-2")
	require.NoError(t, err)
	assert.Equal(t, agent2Time.UTC().Format(time.RFC3339), hb2.UTC().Format(time.RFC3339))
}

// TestSessionService_GetActiveSessionID_ReturnsSessionID verifies active session retrieval.
func TestSessionService_GetActiveSessionID_ReturnsSessionID(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Create an active session.
	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Retrieve active session ID.
	activeID, err := sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, sessionID, activeID)
}

// TestSessionService_GetActiveSessionID_NoActiveSession_ReturnsError verifies error.
func TestSessionService_GetActiveSessionID_NoActiveSession_ReturnsError(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Attempt to get active session when none exists.
	_, err := sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNoActiveSession)
}

// TestSessionService_GetActiveSessionID_AfterSessionEnd_ReturnsError verifies error.
func TestSessionService_GetActiveSessionID_AfterSessionEnd_ReturnsError(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Create and end a session.
	_, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	require.NoError(t, sessionSvc.SessionEnd(ctx, "agent-1"))

	// Attempt to get active session (should fail).
	_, err = sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNoActiveSession)
}

// TestSessionService_GetActiveSessionID_MultipleAgents verifies isolation.
func TestSessionService_GetActiveSessionID_MultipleAgents(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)
	registerAgent(t, s, "agent-2", "PROJ", now)

	// Create sessions for both agents.
	session1ID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	session2ID, err := sessionSvc.SessionStart(ctx, "agent-2", "PROJ")
	require.NoError(t, err)

	// Verify each agent retrieves their own session.
	activeID1, err := sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, session1ID, activeID1)

	activeID2, err := sessionSvc.GetActiveSessionID(ctx, "agent-2")
	require.NoError(t, err)
	assert.Equal(t, session2ID, activeID2)
}

// TestNoopBroadcaster_Broadcast_ReturnsNilError verifies noop behavior.
func TestNoopBroadcaster_Broadcast_ReturnsNilError(t *testing.T) {
	broadcaster := &service.NoopBroadcaster{}
	ctx := context.Background()

	event := service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "test-node",
	}

	err := broadcaster.Broadcast(ctx, event)
	assert.NoError(t, err)
}

// TestNoopBroadcaster_MultipleEvents_StaysNoOp verifies idempotency.
func TestNoopBroadcaster_MultipleEvents_StaysNoOp(t *testing.T) {
	broadcaster := &service.NoopBroadcaster{}
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		event := service.Event{
			Type:   service.EventNodeUpdated,
			NodeID: "node-1",
		}
		err := broadcaster.Broadcast(ctx, event)
		assert.NoError(t, err)
	}
}

// TestConfigService_MaxRecommendedDepth_ReturnsDefault verifies default value.
func TestConfigService_MaxRecommendedDepth_ReturnsDefault(t *testing.T) {
	cfg := &service.StaticConfig{}
	assert.Equal(t, 50, cfg.MaxRecommendedDepth())
}

// TestConfigService_MaxRecommendedDepth_WithCustomValue verifies custom value.
func TestConfigService_MaxRecommendedDepth_WithCustomValue(t *testing.T) {
	cfg := &service.StaticConfig{RecommendedMaxDepth: 100}
	assert.Equal(t, 100, cfg.MaxRecommendedDepth())
}

// TestConfigService_MaxRecommendedDepth_ZeroUsesDefault verifies zero fallback.
func TestConfigService_MaxRecommendedDepth_ZeroUsesDefault(t *testing.T) {
	cfg := &service.StaticConfig{RecommendedMaxDepth: 0}
	assert.Equal(t, 50, cfg.MaxRecommendedDepth())
}

// TestStaticConfig_AutoClaim_DefaultsFalse verifies default.
func TestStaticConfig_AutoClaim_DefaultsFalse(t *testing.T) {
	cfg := &service.StaticConfig{}
	assert.False(t, cfg.AutoClaim())
}

// TestStaticConfig_AutoClaim_ReturnsConfigured verifies set value.
func TestStaticConfig_AutoClaim_ReturnsConfigured(t *testing.T) {
	cfg := &service.StaticConfig{AutoClaimEnabled: true}
	assert.True(t, cfg.AutoClaim())
}

// TestStaticConfig_SoftDeleteRetention_UsesDefault verifies default.
func TestStaticConfig_SoftDeleteRetention_UsesDefault(t *testing.T) {
	cfg := &service.StaticConfig{}
	assert.Equal(t, 30*24*time.Hour, cfg.SoftDeleteRetention())
}

// TestStaticConfig_SoftDeleteRetention_UsesConfigured verifies custom value.
func TestStaticConfig_SoftDeleteRetention_UsesConfigured(t *testing.T) {
	cfg := &service.StaticConfig{RetentionDuration: 7 * 24 * time.Hour}
	assert.Equal(t, 7*24*time.Hour, cfg.SoftDeleteRetention())
}

// TestStaticConfig_AgentStaleThreshold_UsesDefault verifies default.
func TestStaticConfig_AgentStaleThreshold_UsesDefault(t *testing.T) {
	cfg := &service.StaticConfig{}
	assert.Equal(t, 24*time.Hour, cfg.AgentStaleThreshold())
}

// TestStaticConfig_AgentStaleThreshold_UsesConfigured verifies custom value.
func TestStaticConfig_AgentStaleThreshold_UsesConfigured(t *testing.T) {
	cfg := &service.StaticConfig{StaleThreshold: 12 * time.Hour}
	assert.Equal(t, 12*time.Hour, cfg.AgentStaleThreshold())
}

// TestStaticConfig_AgentStuckTimeout_ReturnsConfigured verifies stuck timeout.
func TestStaticConfig_AgentStuckTimeout_ReturnsConfigured(t *testing.T) {
	cfg := &service.StaticConfig{StuckTimeout: 30 * time.Minute}
	assert.Equal(t, 30*time.Minute, cfg.AgentStuckTimeout())
}

// TestStaticConfig_SessionTimeout_UsesDefault verifies default.
func TestStaticConfig_SessionTimeout_UsesDefault(t *testing.T) {
	cfg := &service.StaticConfig{}
	assert.Equal(t, 4*time.Hour, cfg.SessionTimeout())
}

// TestStaticConfig_SessionTimeout_UsesConfigured verifies custom value.
func TestStaticConfig_SessionTimeout_UsesConfigured(t *testing.T) {
	cfg := &service.StaticConfig{SessionTimeoutDur: 8 * time.Hour}
	assert.Equal(t, 8*time.Hour, cfg.SessionTimeout())
}

// TestNodeService_UpdateNode_TitleEmptyString_ReturnsError verifies title validation.
func TestNodeService_UpdateNode_TitleEmptyString_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Original",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Attempt to set empty title.
	emptyTitle := ""
	err = svc.UpdateNode(ctx, node.ID, &store.NodeUpdate{
		Title: &emptyTitle,
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestNodeService_UpdateNode_TitleTooLong_ReturnsError verifies length validation.
func TestNodeService_UpdateNode_TitleTooLong_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Original",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Attempt to set title that's too long.
	longTitle := string(make([]byte, model.MaxTitleLength+1))
	for i := range longTitle {
		longTitle = longTitle[:i] + "x" + longTitle[i+1:]
	}
	err = svc.UpdateNode(ctx, node.ID, &store.NodeUpdate{
		Title: &longTitle,
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestNodeService_UpdateNode_BroadcastsEvent verifies event broadcast.
func TestNodeService_UpdateNode_BroadcastsEvent(t *testing.T) {
	svc, _, bc := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Original",
		Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()

	// Update title.
	newTitle := "Updated"
	err = svc.UpdateNode(ctx, node.ID, &store.NodeUpdate{
		Title: &newTitle,
	})
	require.NoError(t, err)

	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventNodeUpdated, events[0].Type)
	assert.Equal(t, node.ID, events[0].NodeID)
}

// TestNodeService_GetNode_BroadcastsNoEvent verifies query doesn't broadcast.
func TestNodeService_GetNode_BroadcastsNoEvent(t *testing.T) {
	svc, _, bc := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()

	// Retrieve node.
	_, err = svc.GetNode(ctx, node.ID)
	require.NoError(t, err)

	// No event should be broadcast.
	events := bc.Events()
	assert.Len(t, events, 0)
}

// TestAgentService_GetCurrentWork_NoWork_ReturnsError verifies error when no current work.
func TestAgentService_GetCurrentWork_NoWork_ReturnsError(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Get current work when none is assigned.
	_, err := agentSvc.GetCurrentWork(ctx, "agent-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestAgentService_UpdateAgentState_EmitsStateChangeEvent verifies event broadcast.
func TestAgentService_UpdateAgentState_EmitsStateChangeEvent(t *testing.T) {
	agentSvc, _, s, bc := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)
	bc.Reset()

	err := agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking)
	require.NoError(t, err)

	events := bc.Events()
	require.GreaterOrEqual(t, len(events), 1)
	assert.Equal(t, service.EventAgentStateChanged, events[0].Type)
}

// TestSessionService_SessionStart_CreatesULIDSession verifies ULID format.
func TestSessionService_SessionStart_CreatesULIDSession(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// ULID should be 26 characters.
	assert.Len(t, sessionID, 26)

	// Verify it's a valid ULID format (alphanumeric only).
	for _, ch := range sessionID {
		assert.True(t, (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9'),
			"ULID should contain only alphanumeric characters")
	}
}

// TestSessionService_SessionSummary_NotFound_ReturnsError verifies error.
func TestSessionService_SessionSummary_NotFound_ReturnsError(t *testing.T) {
	sessionSvc, _, _ := newTestSessionService(t)
	ctx := context.Background()

	_, err := sessionSvc.SessionSummary(ctx, "nonexistent-agent")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestAgentService_Heartbeat_UpdatesAllAgents verifies per-agent updates.
func TestAgentService_Heartbeat_UpdatesAllAgents(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now.Add(-1*time.Hour))
	registerAgent(t, s, "agent-2", "PROJ", now.Add(-2*time.Hour))

	// Update heartbeat for agent-1.
	require.NoError(t, agentSvc.Heartbeat(ctx, "agent-1"))

	// Verify agent-1 was updated.
	hb1, err := agentSvc.GetLastHeartbeat(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, now.UTC().Format(time.RFC3339), hb1.UTC().Format(time.RFC3339))

	// Verify agent-2 was not affected.
	hb2, err := agentSvc.GetLastHeartbeat(ctx, "agent-2")
	require.NoError(t, err)
	assert.Equal(t, now.Add(-2*time.Hour).UTC().Format(time.RFC3339), hb2.UTC().Format(time.RFC3339))
}

// TestNodeService_CreateNode_DefaultPriority verifies default priority assignment.
func TestNodeService_CreateNode_DefaultPriority(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	req := &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "No Priority",
		Creator: "admin",
		Priority: 0, // No priority specified.
	}

	node, err := svc.CreateNode(ctx, req)
	require.NoError(t, err)

	// Should default to medium priority.
	assert.Equal(t, model.PriorityMedium, node.Priority)

	// Verify in store.
	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PriorityMedium, got.Priority)
}

// TestNodeService_CreateNode_WeightDefaultsToOne verifies weight default.
func TestNodeService_CreateNode_WeightDefaultsToOne(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Weight Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, 1.0, got.Weight)
}

// TestNodeService_CreateNode_ContentHashComputed verifies hash generation.
func TestNodeService_CreateNode_ContentHashComputed(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Hash Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, got.ContentHash)
	assert.Len(t, got.ContentHash, 64) // SHA-256 in hex is 64 chars.
}

// TestNodeService_DeleteNode_BroadcastsEvent verifies event broadcast.
func TestNodeService_DeleteNode_BroadcastsEvent(t *testing.T) {
	svc, _, bc := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "To Delete",
		Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()

	err = svc.DeleteNode(ctx, node.ID, false, "admin")
	require.NoError(t, err)

	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventNodeDeleted, events[0].Type)
	assert.Equal(t, "admin", events[0].Author)
}

// TestNodeService_TransitionStatus_BroadcastsEvent verifies event broadcast.
func TestNodeService_TransitionStatus_BroadcastsEvent(t *testing.T) {
	svc, s, bc := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Transition Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Claim the node to prepare for transition.
	require.NoError(t, s.ClaimNode(ctx, node.ID, "agent-1"))

	bc.Reset()

	err = svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "starting work", "agent-1")
	require.NoError(t, err)

	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventStatusChanged, events[0].Type)
	assert.Equal(t, "agent-1", events[0].Author)
}
