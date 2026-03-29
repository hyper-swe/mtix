// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package e2e — Issue #1 regression test.
// Verifies agent lifecycle bootstrap gap is fixed per FR-10.1a/FR-10.1b.
// This test reproduces the exact scenario from GitHub issue #1:
// a brand-new agent (never registered) should be able to claim, heartbeat,
// and start sessions without manual registration or FK constraint errors.
package e2e

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestIssue1_AgentBootstrapGap reproduces the exact scenario from GitHub
// issue #1: an unregistered agent should be able to claim nodes, send
// heartbeats, start sessions, and query state — all without manual
// registration or FK constraint errors.
func TestIssue1_AgentBootstrapGap(t *testing.T) {
	env := setupE2E(t)

	// 1. Create a task node in a fresh project.
	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Implement authentication",
		Project: "BOOT",
		Creator: "human-user",
	})
	require.NoError(t, err, "step 1: create node")

	// 2. Claim the node with a brand-new agent (never registered).
	err = env.store.ClaimNode(env.ctx, node.ID, "agent-claude")
	require.NoError(t, err, "step 2: claim should auto-register agent")

	// 3. Verify the agent row was auto-created in the agents table.
	var agentState string
	var currentNodeID sql.NullString
	err = env.store.QueryRow(env.ctx,
		`SELECT state, current_node_id FROM agents WHERE agent_id = ?`,
		"agent-claude",
	).Scan(&agentState, &currentNodeID)
	require.NoError(t, err, "step 3: agent row should exist after claim")
	assert.Equal(t, "working", agentState, "agent state should be 'working' after claim")
	assert.True(t, currentNodeID.Valid, "current_node_id should be set")
	assert.Equal(t, node.ID, currentNodeID.String, "current_node_id should match claimed node")

	// 4. Heartbeat should succeed (agent exists from auto-registration).
	err = env.agentSvc.Heartbeat(env.ctx, "agent-claude")
	require.NoError(t, err, "step 4: heartbeat should work for auto-registered agent")

	// 5. Session start should succeed (auto-registers if needed, no FK error).
	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "agent-claude", "BOOT")
	require.NoError(t, err, "step 5: session start should not produce FK error")
	assert.NotEmpty(t, sessionID, "session ID should be returned")

	// 6. Agent state query should return current state.
	state, err := env.agentSvc.GetAgentState(env.ctx, "agent-claude")
	require.NoError(t, err, "step 6: agent state query should work")
	assert.Equal(t, model.AgentState("working"), state, "agent should be in working state")

	// 7. Agent work query should return the claimed node.
	workNode, err := env.agentSvc.GetCurrentWork(env.ctx, "agent-claude")
	require.NoError(t, err, "step 7: agent work query should work")
	require.NotNil(t, workNode, "agent should have current work")
	assert.Equal(t, node.ID, workNode.ID, "current work should be the claimed node")

	// 8. Unclaim should reset agent to idle.
	err = env.store.UnclaimNode(env.ctx, node.ID, "task reassigned", "agent-claude")
	require.NoError(t, err, "step 8: unclaim should succeed")

	// Verify agent state reset.
	err = env.store.QueryRow(env.ctx,
		`SELECT state, current_node_id FROM agents WHERE agent_id = ?`,
		"agent-claude",
	).Scan(&agentState, &currentNodeID)
	require.NoError(t, err)
	assert.Equal(t, "idle", agentState, "agent state should be 'idle' after unclaim")
	assert.False(t, currentNodeID.Valid, "current_node_id should be NULL after unclaim")

	// 9. Heartbeat for an unknown agent should return an error (phantom prevention).
	err = env.agentSvc.Heartbeat(env.ctx, "unknown-agent-typo")
	assert.Error(t, err, "step 9: heartbeat for non-existent agent should fail")
	assert.ErrorIs(t, err, model.ErrNotFound, "should be ErrNotFound, not silent success")
}
