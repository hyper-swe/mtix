// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newTestAgentService creates an AgentService backed by real SQLite.
func newTestAgentService(t *testing.T) (
	*service.AgentService, *service.NodeService, *sqlite.Store, *recordingBroadcaster,
) {
	t.Helper()

	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{
		AutoClaimEnabled: false,
		StaleThreshold:   24 * time.Hour,
	}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	logger := slog.Default()

	nodeSvc := service.NewNodeService(s, bc, cfg, logger, clock)
	agentSvc := service.NewAgentService(s, bc, cfg, logger, clock)

	return agentSvc, nodeSvc, s, bc
}

// registerAgent is a helper to insert an agent row for testing.
func registerAgent(t *testing.T, s *sqlite.Store, agentID, project string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, ?, 'idle', ?, ?)`,
		agentID, project,
		now.UTC().Format(time.RFC3339),
		now.UTC().Format(time.RFC3339),
	)
	require.NoError(t, err)
}

// TestAgentState_IdleToWorking_Succeeds verifies valid transition.
func TestAgentState_IdleToWorking_Succeeds(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	err := agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking)
	require.NoError(t, err)

	// Verify state changed.
	state, err := agentSvc.GetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateWorking, state)
}

// TestAgentState_WorkingToStuck_BroadcastsEvent verifies stuck event.
func TestAgentState_WorkingToStuck_BroadcastsEvent(t *testing.T) {
	agentSvc, _, s, bc := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// Transition to working first.
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking))
	bc.Reset()

	// Transition to stuck.
	err := agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateStuck)
	require.NoError(t, err)

	events := bc.Events()
	require.GreaterOrEqual(t, len(events), 1)

	var found bool
	for _, e := range events {
		if e.Type == service.EventAgentStuck {
			found = true
			break
		}
	}
	assert.True(t, found, "should broadcast agent.stuck event")
}

// TestAgentState_InvalidTransition_ReturnsError verifies rejection.
func TestAgentState_InvalidTransition_ReturnsError(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// idle → done is not valid (must go through working first).
	err := agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateDone)
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestHeartbeat_UpdatesTimestamp verifies heartbeat tracking.
func TestHeartbeat_UpdatesTimestamp(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	pastTime := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", pastTime)

	err := agentSvc.Heartbeat(ctx, "agent-1")
	require.NoError(t, err)

	// Verify heartbeat was updated (should use injected clock).
	hb, err := agentSvc.GetLastHeartbeat(ctx, "agent-1")
	require.NoError(t, err)
	expected := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, expected.UTC().Format(time.RFC3339), hb.UTC().Format(time.RFC3339))
}

// TestStaleAgents_OlderThanThreshold_Returned verifies stale detection.
func TestStaleAgents_OlderThanThreshold_Returned(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Agent with old heartbeat (25h ago).
	oldTime := now.Add(-25 * time.Hour)
	registerAgent(t, s, "agent-stale", "PROJ", oldTime)

	stale, err := agentSvc.GetStaleAgents(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "agent-stale", stale[0])
}

// TestStaleAgents_RecentHeartbeat_NotReturned verifies fresh agents excluded.
func TestStaleAgents_RecentHeartbeat_NotReturned(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Agent with recent heartbeat.
	registerAgent(t, s, "agent-fresh", "PROJ", now)

	stale, err := agentSvc.GetStaleAgents(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Len(t, stale, 0)
}

// TestStuckTimeout_AutoUnclaims_WhenConfigured verifies FR-10.3a auto-unclaim.
func TestStuckTimeout_AutoUnclaims_WhenConfigured(t *testing.T) {
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

	// Register agent and create a node.
	registerAgent(t, s, "agent-1", "PROJ", now)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Stuck Test", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim the node — this auto-sets agent to 'working' per FR-10.1b.
	require.NoError(t, s.ClaimNode(ctx, node.ID, "agent-1"))

	// Transition from working → stuck.
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateStuck))

	// Manually set state_changed_at to 31 minutes ago.
	db := s.WriteDB()
	stuckTime := now.Add(-31 * time.Minute).UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET state_changed_at = ?, current_node_id = ? WHERE agent_id = ?`,
		stuckTime, node.ID, "agent-1",
	)
	require.NoError(t, err)

	// Run stuck timeout check.
	err = agentSvc.CheckStuckTimeouts(ctx)
	require.NoError(t, err)

	// Node should be unclaimed.
	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Empty(t, got.Assignee)
}

// TestNewAgentService_NilBroadcaster_UsesNoop verifies nil broadcaster fallback.
func TestNewAgentService_NilBroadcaster_UsesNoop(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	// Pass nil broadcaster, config, and logger — should not panic.
	svc := service.NewAgentService(s, nil, nil, nil, fixedClock(now))
	require.NotNil(t, svc)

	// Verify the service still works with noop defaults.
	ctx := context.Background()
	registerAgent(t, s, "agent-nil", "PROJ", now)
	err = svc.UpdateAgentState(ctx, "agent-nil", model.AgentStateWorking)
	assert.NoError(t, err)
}

// TestGetCurrentWork_NonExistentAgent_ReturnsNotFound verifies error for unknown agent.
func TestGetCurrentWork_NonExistentAgent_ReturnsNotFound(t *testing.T) {
	agentSvc, _, _, _ := newTestAgentService(t)
	ctx := context.Background()

	_, err := agentSvc.GetCurrentWork(ctx, "nonexistent-agent")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestCheckStuckTimeouts_NoTimeout_ReturnsNilImmediately verifies early exit.
func TestCheckStuckTimeouts_NoTimeout_ReturnsNilImmediately(t *testing.T) {
	// Use a config with zero stuck timeout (disabled).
	agentSvc, _, _, _ := newTestAgentService(t)
	ctx := context.Background()

	err := agentSvc.CheckStuckTimeouts(ctx)
	assert.NoError(t, err)
}

// TestCheckStuckTimeouts_NoStuckAgents_ReturnsNil verifies no-op when no agents stuck.
func TestCheckStuckTimeouts_NoStuckAgents_ReturnsNil(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{StuckTimeout: 30 * time.Minute}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewAgentService(s, nil, cfg, nil, fixedClock(now))

	err = svc.CheckStuckTimeouts(context.Background())
	assert.NoError(t, err)
}

// TestAgentState_DoneToIdle_Succeeds verifies valid transition per FR-10.2.
func TestAgentState_DoneToIdle_Succeeds(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	// idle → working → done → idle
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking))
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateDone))
	err := agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateIdle)
	assert.NoError(t, err)

	state, err := agentSvc.GetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateIdle, state)
}

// TestAgentState_StuckToWorking_Succeeds verifies recovery from stuck.
func TestAgentState_StuckToWorking_Succeeds(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking))
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateStuck))

	err := agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateWorking)
	assert.NoError(t, err)
}

// TestAgentState_UnknownFromState_ReturnsInvalidTransition verifies unknown state rejection.
func TestAgentState_UnknownFromState_ReturnsInvalidTransition(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Register agent with an unusual state via direct SQL.
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, ?, 'unknown_state', ?, ?)`,
		"agent-bad", "PROJ",
		now.UTC().Format(time.RFC3339),
		now.UTC().Format(time.RFC3339),
	)
	require.NoError(t, err)

	err = agentSvc.UpdateAgentState(ctx, "agent-bad", model.AgentStateWorking)
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestGetStaleAgents_ClosedStore_ReturnsError verifies store error propagation.
func TestGetStaleAgents_ClosedStore_ReturnsError(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewAgentService(s, nil, nil, nil, fixedClock(now))

	require.NoError(t, s.Close())

	_, err = svc.GetStaleAgents(context.Background(), 24*time.Hour)
	assert.Error(t, err)
}

// TestCheckStuckTimeouts_ClosedStore_ReturnsError verifies store error handling.
func TestCheckStuckTimeouts_ClosedStore_ReturnsError(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)

	cfg := &service.StaticConfig{StuckTimeout: 30 * time.Minute}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewAgentService(s, nil, cfg, nil, fixedClock(now))

	require.NoError(t, s.Close())

	err = svc.CheckStuckTimeouts(context.Background())
	assert.Error(t, err)
}

// TestGetCurrentWork_ReturnsClaimedNode verifies current work retrieval.
func TestGetCurrentWork_ReturnsClaimedNode(t *testing.T) {
	agentSvc, nodeSvc, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Current Work", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim and set current_node_id.
	require.NoError(t, s.ClaimNode(ctx, node.ID, "agent-1"))
	db := s.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`,
		node.ID, "agent-1",
	)
	require.NoError(t, err)

	got, err := agentSvc.GetCurrentWork(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, node.ID, got.ID)
}
