// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestRegisterAgent_NewAgent_Succeeds verifies agent registration creates a row.
func TestRegisterAgent_NewAgent_Succeeds(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()

	err := agentSvc.RegisterAgent(ctx, "agent-new", "PROJ")
	require.NoError(t, err)

	// Verify agent row exists with correct initial state.
	var state, project string
	err = s.QueryRow(ctx,
		`SELECT state, project FROM agents WHERE agent_id = ?`, "agent-new",
	).Scan(&state, &project)
	require.NoError(t, err)
	assert.Equal(t, "idle", state)
	assert.Equal(t, "PROJ", project)
}

// TestRegisterAgent_Duplicate_ReturnsAlreadyExists verifies idempotency guard.
func TestRegisterAgent_Duplicate_ReturnsAlreadyExists(t *testing.T) {
	agentSvc, _, _, _ := newTestAgentService(t)
	ctx := context.Background()

	err := agentSvc.RegisterAgent(ctx, "agent-dup", "PROJ")
	require.NoError(t, err)

	// Second registration should return ErrAlreadyExists.
	err = agentSvc.RegisterAgent(ctx, "agent-dup", "PROJ")
	assert.ErrorIs(t, err, model.ErrAlreadyExists)
}

// TestEnsureAgent_NewAgent_CreatesRow verifies idempotent creation.
func TestEnsureAgent_NewAgent_CreatesRow(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()

	err := agentSvc.EnsureAgent(ctx, "agent-ensure", "PROJ")
	require.NoError(t, err)

	// Verify row exists.
	var state string
	err = s.QueryRow(ctx,
		`SELECT state FROM agents WHERE agent_id = ?`, "agent-ensure",
	).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, "idle", state)
}

// TestEnsureAgent_ExistingAgent_NoOp verifies no error on duplicate.
func TestEnsureAgent_ExistingAgent_NoOp(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-existing", "PROJ", now)

	// EnsureAgent should be a silent no-op for existing agents.
	err := agentSvc.EnsureAgent(ctx, "agent-existing", "PROJ")
	assert.NoError(t, err)

	// Verify original state is preserved (not overwritten).
	state, err := agentSvc.GetAgentState(ctx, "agent-existing")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateIdle, state)
}

// TestHeartbeat_NonExistentAgent_ReturnsNotFound verifies phantom prevention.
func TestHeartbeat_NonExistentAgent_ReturnsNotFound(t *testing.T) {
	agentSvc, _, _, _ := newTestAgentService(t)
	ctx := context.Background()

	err := agentSvc.Heartbeat(ctx, "ghost-agent")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestHeartbeat_ExistingAgent_UpdatesTimestamp verifies heartbeat for registered agent.
func TestHeartbeat_ExistingAgent_UpdatesTimestamp(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	past := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-hb", "PROJ", past)

	err := agentSvc.Heartbeat(ctx, "agent-hb")
	require.NoError(t, err)

	hb, err := agentSvc.GetLastHeartbeat(ctx, "agent-hb")
	require.NoError(t, err)
	// Should use injected clock (2026-03-10T12:00:00Z), not past time.
	expected := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, expected.UTC().Format(time.RFC3339), hb.UTC().Format(time.RFC3339))
}

// TestClaimNode_AutoRegistersAgent verifies claim creates agent row per FR-10.1a.
func TestClaimNode_AutoRegistersAgent(t *testing.T) {
	_, nodeSvc, s, _ := newTestAgentService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Auto-register test", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim with an agent that doesn't exist yet.
	err = s.ClaimNode(ctx, node.ID, "brand-new-agent")
	require.NoError(t, err)

	// Verify agent row was auto-created.
	var agentState string
	err = s.QueryRow(ctx,
		`SELECT state FROM agents WHERE agent_id = ?`, "brand-new-agent",
	).Scan(&agentState)
	require.NoError(t, err, "agent row should exist after claim")
}

// TestClaimNode_UpdatesAgentCurrentNodeID verifies agents.current_node_id sync.
func TestClaimNode_UpdatesAgentCurrentNodeID(t *testing.T) {
	_, nodeSvc, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-claim", "PROJ", now)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Current node test", Creator: "admin",
	})
	require.NoError(t, err)

	err = s.ClaimNode(ctx, node.ID, "agent-claim")
	require.NoError(t, err)

	// Verify agents.current_node_id was set.
	var currentNodeID sql.NullString
	err = s.QueryRow(ctx,
		`SELECT current_node_id FROM agents WHERE agent_id = ?`, "agent-claim",
	).Scan(&currentNodeID)
	require.NoError(t, err)
	assert.True(t, currentNodeID.Valid)
	assert.Equal(t, node.ID, currentNodeID.String)
}

// TestClaimNode_UpdatesAgentState_Working verifies agents.state sync.
func TestClaimNode_UpdatesAgentState_Working(t *testing.T) {
	_, nodeSvc, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-state", "PROJ", now)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "State sync test", Creator: "admin",
	})
	require.NoError(t, err)

	err = s.ClaimNode(ctx, node.ID, "agent-state")
	require.NoError(t, err)

	// Verify agent state is "working".
	var state string
	err = s.QueryRow(ctx,
		`SELECT state FROM agents WHERE agent_id = ?`, "agent-state",
	).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, "working", state)
}

// TestUnclaimNode_ResetsAgentCurrentNodeID verifies unclaim resets agent state.
func TestUnclaimNode_ResetsAgentCurrentNodeID(t *testing.T) {
	_, nodeSvc, s, _ := newTestAgentService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-unclaim", "PROJ", now)

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Unclaim test", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim then unclaim.
	err = s.ClaimNode(ctx, node.ID, "agent-unclaim")
	require.NoError(t, err)
	err = s.UnclaimNode(ctx, node.ID, "done with it", "agent-unclaim")
	require.NoError(t, err)

	// Verify agent is reset.
	var currentNodeID sql.NullString
	var state string
	err = s.QueryRow(ctx,
		`SELECT current_node_id, state FROM agents WHERE agent_id = ?`, "agent-unclaim",
	).Scan(&currentNodeID, &state)
	require.NoError(t, err)
	assert.False(t, currentNodeID.Valid, "current_node_id should be NULL after unclaim")
	assert.Equal(t, "idle", state, "agent state should be idle after unclaim")
}
