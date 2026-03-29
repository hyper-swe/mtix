// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package e2e — Agent lifecycle E2E tests for mission-critical scenarios.
// Validates FR-10.1a (agent registration), FR-10.1b (agent-node consistency),
// FR-10.4 (claim/unclaim), FR-10.4a (force-reclaim), FR-10.5a (sessions).
package e2e

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestE2E_AgentLifecycle_FullCycle validates the complete agent lifecycle:
// register → claim → heartbeat → session → done → session end → unclaim.
func TestE2E_AgentLifecycle_FullCycle(t *testing.T) {
	env := setupE2E(t)

	// Register agent explicitly.
	err := env.agentSvc.RegisterAgent(env.ctx, "lifecycle-agent", "LIFE")
	require.NoError(t, err)

	// Create a task.
	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Full lifecycle task", Project: "LIFE", Creator: "admin",
	})
	require.NoError(t, err)

	// Start session.
	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "lifecycle-agent", "LIFE")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)

	// Claim the node.
	err = env.store.ClaimNode(env.ctx, node.ID, "lifecycle-agent")
	require.NoError(t, err)

	// Heartbeat.
	err = env.agentSvc.Heartbeat(env.ctx, "lifecycle-agent")
	require.NoError(t, err)

	// Complete the node.
	err = env.nodeSvc.TransitionStatus(env.ctx, node.ID, model.StatusDone,
		"completed", "lifecycle-agent")
	require.NoError(t, err)

	// End session.
	err = env.sessionSvc.SessionEnd(env.ctx, "lifecycle-agent")
	require.NoError(t, err)

	// Verify session summary.
	summary, err := env.sessionSvc.SessionSummary(env.ctx, "lifecycle-agent")
	require.NoError(t, err)
	assert.Equal(t, "ended", summary.Status)
}

// TestE2E_AgentLifecycle_BootstrapWithoutRegister validates that claim
// auto-registers the agent, and all subsequent commands work.
func TestE2E_AgentLifecycle_BootstrapWithoutRegister(t *testing.T) {
	env := setupE2E(t)

	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Auto-register test", Project: "AUTO", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim without prior registration — should auto-register.
	err = env.store.ClaimNode(env.ctx, node.ID, "auto-agent")
	require.NoError(t, err)

	// All subsequent operations should work.
	err = env.agentSvc.Heartbeat(env.ctx, "auto-agent")
	require.NoError(t, err)

	state, err := env.agentSvc.GetAgentState(env.ctx, "auto-agent")
	require.NoError(t, err)
	assert.Equal(t, model.AgentState("working"), state)

	work, err := env.agentSvc.GetCurrentWork(env.ctx, "auto-agent")
	require.NoError(t, err)
	require.NotNil(t, work)
	assert.Equal(t, node.ID, work.ID)

	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "auto-agent", "AUTO")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
}

// TestE2E_AgentLifecycle_MultiAgentConcurrentClaim validates two agents
// claiming different nodes — both get agent rows and correct state.
func TestE2E_AgentLifecycle_MultiAgentConcurrentClaim(t *testing.T) {
	env := setupE2E(t)

	node1, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Task 1", Project: "MULTI", Creator: "admin",
	})
	require.NoError(t, err)

	node2, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Task 2", Project: "MULTI", Creator: "admin",
	})
	require.NoError(t, err)

	// Both agents claim different nodes (no prior registration).
	err = env.store.ClaimNode(env.ctx, node1.ID, "agent-alpha")
	require.NoError(t, err)
	err = env.store.ClaimNode(env.ctx, node2.ID, "agent-beta")
	require.NoError(t, err)

	// Both agents should have rows in the agents table.
	var stateAlpha, stateBeta string
	var nodeAlpha, nodeBeta sql.NullString
	err = env.store.QueryRow(env.ctx,
		`SELECT state, current_node_id FROM agents WHERE agent_id = ?`, "agent-alpha",
	).Scan(&stateAlpha, &nodeAlpha)
	require.NoError(t, err)
	assert.Equal(t, "working", stateAlpha)
	assert.Equal(t, node1.ID, nodeAlpha.String)

	err = env.store.QueryRow(env.ctx,
		`SELECT state, current_node_id FROM agents WHERE agent_id = ?`, "agent-beta",
	).Scan(&stateBeta, &nodeBeta)
	require.NoError(t, err)
	assert.Equal(t, "working", stateBeta)
	assert.Equal(t, node2.ID, nodeBeta.String)
}

// TestE2E_AgentLifecycle_StateConsistency validates that claim sets working,
// unclaim resets idle, done transitions correctly.
func TestE2E_AgentLifecycle_StateConsistency(t *testing.T) {
	env := setupE2E(t)

	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "State consistency test", Project: "STATE", Creator: "admin",
	})
	require.NoError(t, err)

	// Claim sets agent to working.
	err = env.store.ClaimNode(env.ctx, node.ID, "state-agent")
	require.NoError(t, err)

	state, err := env.agentSvc.GetAgentState(env.ctx, "state-agent")
	require.NoError(t, err)
	assert.Equal(t, model.AgentState("working"), state)

	// Unclaim resets to idle.
	err = env.store.UnclaimNode(env.ctx, node.ID, "testing reset", "state-agent")
	require.NoError(t, err)

	state, err = env.agentSvc.GetAgentState(env.ctx, "state-agent")
	require.NoError(t, err)
	assert.Equal(t, model.AgentState("idle"), state)

	// Re-claim, then done.
	err = env.store.ClaimNode(env.ctx, node.ID, "state-agent")
	require.NoError(t, err)
	err = env.nodeSvc.TransitionStatus(env.ctx, node.ID, model.StatusDone,
		"finished", "state-agent")
	require.NoError(t, err)

	// Node should be done.
	fetched, err := env.nodeSvc.GetNode(env.ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, fetched.Status)
}

// TestE2E_AgentLifecycle_SessionFKIntegrity validates that session start
// always succeeds via auto-registration (prevents FK error).
func TestE2E_AgentLifecycle_SessionFKIntegrity(t *testing.T) {
	env := setupE2E(t)

	// Start session for brand-new agent — should auto-register, no FK error.
	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "session-agent", "SESS")
	require.NoError(t, err, "session start should not produce FK error")
	assert.NotEmpty(t, sessionID)

	// Verify agent was auto-registered.
	var agentState string
	err = env.store.QueryRow(env.ctx,
		`SELECT state FROM agents WHERE agent_id = ?`, "session-agent",
	).Scan(&agentState)
	require.NoError(t, err, "agent should exist after session start")
	assert.Equal(t, "idle", agentState)

	// End session should also work.
	err = env.sessionSvc.SessionEnd(env.ctx, "session-agent")
	require.NoError(t, err)
}

// TestE2E_AgentLifecycle_ExportImportRoundtrip validates that agents and
// sessions survive export → import replace cycle.
func TestE2E_AgentLifecycle_ExportImportRoundtrip(t *testing.T) {
	env := setupE2E(t)

	// Register agent and start a session.
	err := env.agentSvc.RegisterAgent(env.ctx, "export-agent", "EXP")
	require.NoError(t, err)

	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "export-agent", "EXP")
	require.NoError(t, err)

	// Create and claim a node.
	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Export test", Project: "EXP", Creator: "admin",
	})
	require.NoError(t, err)

	err = env.store.ClaimNode(env.ctx, node.ID, "export-agent")
	require.NoError(t, err)

	// End session.
	err = env.sessionSvc.SessionEnd(env.ctx, "export-agent")
	require.NoError(t, err)

	// Export.
	data, err := env.sqlStore.Export(env.ctx, "EXP", "test")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(data.Agents), 1, "export should include agents")
	assert.GreaterOrEqual(t, len(data.Sessions), 1, "export should include sessions")

	// Import replace into the same DB.
	result, err := env.sqlStore.Import(env.ctx, data, "replace")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.NodesCreated, 1)

	// Verify agents survived the roundtrip.
	var state string
	err = env.store.QueryRow(env.ctx,
		`SELECT state FROM agents WHERE agent_id = ?`, "export-agent",
	).Scan(&state)
	require.NoError(t, err, "agent should survive import replace")

	// Verify sessions survived.
	var sessStatus string
	err = env.store.QueryRow(env.ctx,
		`SELECT status FROM sessions WHERE id = ?`, sessionID,
	).Scan(&sessStatus)
	require.NoError(t, err, "session should survive import replace")
	assert.Equal(t, "ended", sessStatus)
}

// TestE2E_AgentLifecycle_HeartbeatPhantomPrevention validates that heartbeat
// for a non-existent agent returns an error (no phantom row created).
func TestE2E_AgentLifecycle_HeartbeatPhantomPrevention(t *testing.T) {
	env := setupE2E(t)

	// Heartbeat for an agent that was never registered.
	err := env.agentSvc.Heartbeat(env.ctx, "phantom-agent-typo")
	require.Error(t, err, "heartbeat for non-existent agent should fail")
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Verify no phantom row was created.
	var count int
	err = env.store.QueryRow(env.ctx,
		`SELECT COUNT(*) FROM agents WHERE agent_id = ?`, "phantom-agent-typo",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no phantom agent row should be created")
}

// TestE2E_AgentLifecycle_StaleAgentDetection validates that a registered
// agent with an old heartbeat appears in the stale report.
func TestE2E_AgentLifecycle_StaleAgentDetection(t *testing.T) {
	env := setupE2E(t)

	// Register agent and claim a node.
	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Stale test", Project: "STALE", Creator: "admin",
	})
	require.NoError(t, err)

	err = env.store.ClaimNode(env.ctx, node.ID, "stale-agent")
	require.NoError(t, err)

	// Backdate heartbeat relative to the fixed test clock (2026-03-10T12:00:00Z).
	fixedNow := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	staleTime := fixedNow.Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	_, err = env.store.WriteDB().ExecContext(env.ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		staleTime, "stale-agent",
	)
	require.NoError(t, err)

	// Query stale agents — should include stale-agent.
	staleAgents, err := env.agentSvc.GetStaleAgents(env.ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Contains(t, staleAgents, "stale-agent", "stale-agent should appear in stale report")
}

// TestE2E_AgentLifecycle_ForceReclaimWithAgentSync validates that
// force-reclaim updates both nodes.assignee AND agents.current_node_id,
// and resets the old agent to idle.
func TestE2E_AgentLifecycle_ForceReclaimWithAgentSync(t *testing.T) {
	env := setupE2E(t)

	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Reclaim sync test", Project: "RCLM", Creator: "admin",
	})
	require.NoError(t, err)

	// Agent-1 claims the node.
	err = env.store.ClaimNode(env.ctx, node.ID, "old-agent")
	require.NoError(t, err)

	// Backdate old-agent's heartbeat to make it stale.
	fixedNow := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	staleTime := fixedNow.Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	_, err = env.store.WriteDB().ExecContext(env.ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		staleTime, "old-agent",
	)
	require.NoError(t, err)

	// Agent-2 force-reclaims.
	err = env.store.ForceReclaimNode(env.ctx, node.ID, "new-agent", 24*time.Hour)
	require.NoError(t, err)

	// Verify node assignee updated.
	fetched, err := env.nodeSvc.GetNode(env.ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-agent", fetched.Assignee)

	// Verify old-agent reset to idle with no current node.
	var oldState string
	var oldNodeID sql.NullString
	err = env.store.QueryRow(env.ctx,
		`SELECT state, current_node_id FROM agents WHERE agent_id = ?`, "old-agent",
	).Scan(&oldState, &oldNodeID)
	require.NoError(t, err)
	assert.Equal(t, "idle", oldState, "old agent should be reset to idle")
	assert.False(t, oldNodeID.Valid, "old agent's current_node_id should be NULL")

	// Verify new-agent has the node assigned.
	var newState string
	var newNodeID sql.NullString
	err = env.store.QueryRow(env.ctx,
		`SELECT state, current_node_id FROM agents WHERE agent_id = ?`, "new-agent",
	).Scan(&newState, &newNodeID)
	require.NoError(t, err)
	assert.Equal(t, "working", newState, "new agent should be working")
	assert.Equal(t, node.ID, newNodeID.String, "new agent should have the reclaimed node")
}

// TestE2E_AgentLifecycle_RegisterDuplicate validates that registering
// an already-existing agent returns ErrAlreadyExists.
func TestE2E_AgentLifecycle_RegisterDuplicate(t *testing.T) {
	env := setupE2E(t)

	err := env.agentSvc.RegisterAgent(env.ctx, "dup-agent", "DUP")
	require.NoError(t, err)

	err = env.agentSvc.RegisterAgent(env.ctx, "dup-agent", "DUP")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrAlreadyExists)
}

// TestE2E_AgentLifecycle_ClaimReclaimConsistentState validates that after
// a claim, crash simulation, and re-claim, the agent state is consistent.
func TestE2E_AgentLifecycle_ClaimReclaimConsistentState(t *testing.T) {
	env := setupE2E(t)

	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title: "Crash recovery test", Project: "CRASH", Creator: "admin",
	})
	require.NoError(t, err)

	// Agent claims the node.
	err = env.store.ClaimNode(env.ctx, node.ID, "crash-agent")
	require.NoError(t, err)

	// Simulate agent crash: backdate heartbeat.
	fixedNow := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	staleTime := fixedNow.Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	_, err = env.store.WriteDB().ExecContext(env.ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		staleTime, "crash-agent",
	)
	require.NoError(t, err)

	// New agent force-reclaims.
	err = env.store.ForceReclaimNode(env.ctx, node.ID, "recovery-agent", 24*time.Hour)
	require.NoError(t, err)

	// Verify crash-agent is idle.
	crashState, err := env.agentSvc.GetAgentState(env.ctx, "crash-agent")
	require.NoError(t, err)
	assert.Equal(t, model.AgentState("idle"), crashState)

	// Verify recovery-agent is working on the node.
	recoveryState, err := env.agentSvc.GetAgentState(env.ctx, "recovery-agent")
	require.NoError(t, err)
	assert.Equal(t, model.AgentState("working"), recoveryState)

	work, err := env.agentSvc.GetCurrentWork(env.ctx, "recovery-agent")
	require.NoError(t, err)
	require.NotNil(t, work)
	assert.Equal(t, node.ID, work.ID)
}
