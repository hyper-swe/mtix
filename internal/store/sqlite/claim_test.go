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
)

// TestClaim_OpenNode_Succeeds verifies claim on open node.
func TestClaim_OpenNode_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Open Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-001")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
	assert.Equal(t, "agent-001", got.Assignee)
	assert.Equal(t, model.AgentStateWorking, got.AgentState)
}

// TestClaim_DeferredPastNode_Succeeds verifies claim on deferred node past defer_until.
func TestClaim_DeferredPastNode_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Deferred Node", now)
	node.Status = model.StatusDeferred
	pastTime := now.Add(-time.Hour)
	node.DeferUntil = &pastTime
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-001")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestClaim_DeferredFutureNode_ReturnsStillDeferred verifies future-deferred rejection.
func TestClaim_DeferredFutureNode_ReturnsStillDeferred(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Future Deferred", now)
	node.Status = model.StatusDeferred
	futureTime := now.Add(24 * time.Hour)
	node.DeferUntil = &futureTime
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-001")
	assert.ErrorIs(t, err, model.ErrStillDeferred)
}

// TestClaim_InProgressNode_ReturnsAlreadyClaimed verifies double-claim rejection.
func TestClaim_InProgressNode_ReturnsAlreadyClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Claimed Node", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-002")
	assert.ErrorIs(t, err, model.ErrAlreadyClaimed)
}

// TestClaim_DoneNode_ReturnsInvalidTransition verifies done node rejection.
func TestClaim_DoneNode_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Done Node", now)
	node.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-001")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestUnclaim_RequiresReason verifies mandatory reason for unclaim.
func TestUnclaim_RequiresReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Claimed Node", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UnclaimNode(ctx, "PROJ-1", "", "agent-001")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestUnclaim_SetsStatusToOpen verifies unclaim sets status to open.
func TestUnclaim_SetsStatusToOpen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Claimed Node", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UnclaimNode(ctx, "PROJ-1", "Need to switch tasks", "agent-001")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Empty(t, got.Assignee)
}

// TestForceReclaim_StaleAgent_Succeeds verifies force-reclaim from stale agent.
func TestForceReclaim_StaleAgent_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Register the original agent with an old heartbeat.
	_, err := s.WriteDB().ExecContext(ctx,
		`INSERT INTO agents (agent_id, project, state, last_heartbeat)
		 VALUES (?, ?, ?, ?)`,
		"agent-001", "PROJ", "working",
		now.Add(-48*time.Hour).Format(time.RFC3339),
	)
	require.NoError(t, err)

	node := makeRootNode("PROJ-1", "PROJ", "Stale Claim", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	// Force-reclaim with 24h stale threshold (agent heartbeat is 48h ago).
	err = s.ForceReclaimNode(ctx, "PROJ-1", "agent-002", 24*time.Hour)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-002", got.Assignee)
}

// TestForceReclaim_ActiveAgent_ReturnsAgentStillActive verifies active agent rejection.
func TestForceReclaim_ActiveAgent_ReturnsAgentStillActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Register the original agent with a recent heartbeat.
	_, err := s.WriteDB().ExecContext(ctx,
		`INSERT INTO agents (agent_id, project, state, last_heartbeat)
		 VALUES (?, ?, ?, ?)`,
		"agent-001", "PROJ", "working",
		now.Add(-1*time.Minute).Format(time.RFC3339),
	)
	require.NoError(t, err)

	node := makeRootNode("PROJ-1", "PROJ", "Active Claim", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	// Force-reclaim with 24h threshold — agent heartbeat is 1 minute ago, still active.
	err = s.ForceReclaimNode(ctx, "PROJ-1", "agent-002", 24*time.Hour)
	assert.ErrorIs(t, err, model.ErrAgentStillActive)
}

// TestUnclaim_NonExistent_ReturnsNotFound verifies ErrNotFound for missing nodes.
func TestUnclaim_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UnclaimNode(ctx, "NONEXISTENT-1", "reason", "agent-001")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUnclaim_OpenNode_ReturnsInvalidTransition verifies open->unclaim rejection.
func TestUnclaim_OpenNode_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Open Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UnclaimNode(ctx, "PROJ-1", "reason", "agent-001")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestClaim_NonExistent_ReturnsNotFound verifies ErrNotFound for missing claim.
func TestClaim_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.ClaimNode(ctx, "NONEXISTENT-1", "agent-001")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestClaim_BlockedNode_ReturnsNodeBlocked verifies blocked node claim rejection.
func TestClaim_BlockedNode_ReturnsNodeBlocked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Blocked Node", now)
	node.Status = model.StatusBlocked
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-001")
	assert.ErrorIs(t, err, model.ErrNodeBlocked)
}

// TestClaim_DeferredNoDeferUntil_Succeeds verifies deferred node with no defer_until.
func TestClaim_DeferredNoDeferUntil_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Deferred No Until", now)
	node.Status = model.StatusDeferred
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ClaimNode(ctx, "PROJ-1", "agent-001")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestForceReclaim_NonExistent_ReturnsNotFound verifies missing node rejection.
func TestForceReclaim_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.ForceReclaimNode(ctx, "NONEXISTENT", "agent-002", 24*time.Hour)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestForceReclaim_OpenNode_ReturnsInvalidTransition verifies open node rejection.
func TestForceReclaim_OpenNode_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Open Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.ForceReclaimNode(ctx, "PROJ-1", "agent-002", 24*time.Hour)
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestForceReclaim_NoAgentRecord_TreatsAsStale verifies missing agent treated as stale.
func TestForceReclaim_NoAgentRecord_TreatsAsStale(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a claimed node but don't create an agent record.
	node := makeRootNode("PROJ-1", "PROJ", "Orphaned Claim", now)
	node.Status = model.StatusInProgress
	node.Assignee = "ghost-agent"
	require.NoError(t, s.CreateNode(ctx, node))

	// Force-reclaim should succeed (no agent record = stale).
	err := s.ForceReclaimNode(ctx, "PROJ-1", "agent-002", 24*time.Hour)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-002", got.Assignee)
}
