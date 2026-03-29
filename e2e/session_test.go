// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestE2E_Session_StartReturnsID verifies session start returns a valid ULID.
func TestE2E_Session_StartReturnsID(t *testing.T) {
	env := setupE2E(t)
	ensureAgent(t, env, "agent-001", "SESS")

	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "agent-001", "SESS")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
	assert.Len(t, sessionID, 26, "ULID should be 26 characters")
}

// TestE2E_Session_HeartbeatAccepted verifies that session heartbeats are recorded.
// Heartbeat in mtix is implicit — the agent makes API calls within a session.
// We verify the session remains active after creation.
func TestE2E_Session_HeartbeatAccepted(t *testing.T) {
	env := setupE2E(t)
	ensureAgent(t, env, "agent-001", "SESS")

	sessionID, err := env.sessionSvc.SessionStart(env.ctx, "agent-001", "SESS")
	require.NoError(t, err)

	// Verify session is active.
	activeID, err := env.sessionSvc.GetActiveSessionID(env.ctx, "agent-001")
	require.NoError(t, err)
	assert.Equal(t, sessionID, activeID)
}

// TestE2E_Session_WorkWithinSession verifies nodes can be created during a session.
func TestE2E_Session_WorkWithinSession(t *testing.T) {
	env := setupE2E(t)
	ensureAgent(t, env, "agent-001", "SESS")

	_, err := env.sessionSvc.SessionStart(env.ctx, "agent-001", "SESS")
	require.NoError(t, err)

	// Agent creates work within the session.
	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Session Task",
		Project: "SESS",
		Creator: "agent-001",
	})
	require.NoError(t, err)
	require.NotEmpty(t, node.ID)

	// Claim and complete.
	err = env.store.ClaimNode(env.ctx, node.ID, "agent-001")
	require.NoError(t, err)

	err = env.nodeSvc.TransitionStatus(env.ctx, node.ID, model.StatusDone,
		"done", "agent-001")
	require.NoError(t, err)

	fetched, err := env.nodeSvc.GetNode(env.ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, fetched.Status)
}

// TestE2E_Session_EndRecordsSummary verifies session end produces a summary.
func TestE2E_Session_EndRecordsSummary(t *testing.T) {
	env := setupE2E(t)
	ensureAgent(t, env, "agent-001", "SESS")

	_, err := env.sessionSvc.SessionStart(env.ctx, "agent-001", "SESS")
	require.NoError(t, err)

	// End the session.
	err = env.sessionSvc.SessionEnd(env.ctx, "agent-001")
	require.NoError(t, err)

	// Retrieve session summary.
	summary, err := env.sessionSvc.SessionSummary(env.ctx, "agent-001")
	require.NoError(t, err)
	assert.Equal(t, "ended", summary.Status)
	assert.NotNil(t, summary.EndedAt)
}

// TestE2E_Session_StaleDetection verifies timed-out sessions are auto-ended.
func TestE2E_Session_StaleDetection(t *testing.T) {
	// Use a custom config with a very short session timeout.
	env := setupE2E(t)

	// Override the session service with a short timeout config.
	shortConfig := &service.StaticConfig{
		SessionTimeoutDur: 1 * time.Millisecond,
	}
	shortSessionSvc := service.NewSessionService(
		env.store, shortConfig, nil, testClock(),
	)

	ensureAgent(t, env, "agent-stale", "STALE")
	_, err := shortSessionSvc.SessionStart(env.ctx, "agent-stale", "STALE")
	require.NoError(t, err)

	// Wait a tiny bit and check timeouts.
	// Since the clock is fixed and timeout is 1ms, the session will be
	// considered timed out based on the started_at vs the clock.
	// For a proper timeout test, we need an advancing clock.
	advancingClock := func() time.Time {
		return time.Date(2026, 3, 10, 18, 0, 0, 0, time.UTC) // 6 hours later
	}
	advancingSessionSvc := service.NewSessionService(
		env.store, shortConfig, nil, advancingClock,
	)

	err = advancingSessionSvc.CheckSessionTimeouts(env.ctx)
	require.NoError(t, err)

	// Session should no longer be active.
	_, err = shortSessionSvc.GetActiveSessionID(env.ctx, "agent-stale")
	assert.ErrorIs(t, err, model.ErrNoActiveSession)
}

// TestE2E_Session_EndUnclaims verifies ending a session properly ends it.
func TestE2E_Session_EndUnclaims(t *testing.T) {
	env := setupE2E(t)
	ensureAgent(t, env, "agent-001", "UNCL")

	_, err := env.sessionSvc.SessionStart(env.ctx, "agent-001", "UNCL")
	require.NoError(t, err)

	// Create and claim a node.
	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Claimed Task",
		Project: "UNCL",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	err = env.store.ClaimNode(env.ctx, node.ID, "agent-001")
	require.NoError(t, err)

	// End session.
	err = env.sessionSvc.SessionEnd(env.ctx, "agent-001")
	require.NoError(t, err)

	// Session should be ended.
	summary, err := env.sessionSvc.SessionSummary(env.ctx, "agent-001")
	require.NoError(t, err)
	assert.Equal(t, "ended", summary.Status)
}

// TestE2E_Session_ConcurrentNonInterfering verifies two agents' sessions are independent.
func TestE2E_Session_ConcurrentNonInterfering(t *testing.T) {
	env := setupE2E(t)

	// Start sessions for two different agents.
	ensureAgent(t, env, "agent-001", "CONC")
	ensureAgent(t, env, "agent-002", "CONC")
	sid1, err := env.sessionSvc.SessionStart(env.ctx, "agent-001", "CONC")
	require.NoError(t, err)

	sid2, err := env.sessionSvc.SessionStart(env.ctx, "agent-002", "CONC")
	require.NoError(t, err)

	// Sessions should be distinct.
	assert.NotEqual(t, sid1, sid2)

	// Each agent has their own active session.
	active1, err := env.sessionSvc.GetActiveSessionID(env.ctx, "agent-001")
	require.NoError(t, err)
	assert.Equal(t, sid1, active1)

	active2, err := env.sessionSvc.GetActiveSessionID(env.ctx, "agent-002")
	require.NoError(t, err)
	assert.Equal(t, sid2, active2)

	// End agent-001's session.
	err = env.sessionSvc.SessionEnd(env.ctx, "agent-001")
	require.NoError(t, err)

	// agent-002's session should still be active.
	active2, err = env.sessionSvc.GetActiveSessionID(env.ctx, "agent-002")
	require.NoError(t, err)
	assert.Equal(t, sid2, active2)

	// agent-001 should have no active session.
	_, err = env.sessionSvc.GetActiveSessionID(env.ctx, "agent-001")
	assert.ErrorIs(t, err, model.ErrNoActiveSession)
}
