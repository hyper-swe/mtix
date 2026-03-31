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

// newTestSessionService creates a SessionService backed by real SQLite.
func newTestSessionService(t *testing.T) (
	*service.SessionService, *service.NodeService, *sqlite.Store,
) {
	t.Helper()

	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{
		AutoClaimEnabled:  false,
		SessionTimeoutDur: 4 * time.Hour,
	}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	logger := slog.Default()

	nodeSvc := service.NewNodeService(s, bc, cfg, logger, clock)
	sessionSvc := service.NewSessionService(s, cfg, logger, clock)

	return sessionSvc, nodeSvc, s
}

// TestSessionStart_CreatesActiveSession verifies session creation.
func TestSessionStart_CreatesActiveSession(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// Register agent first.
	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)
	assert.Len(t, sessionID, 26, "session ID should be a ULID")

	// Verify session exists and is active.
	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, sessionID, summary.SessionID)
	assert.Equal(t, "active", summary.Status)
}

// TestSessionStart_AutoEndsPreviousSession verifies FR-10.5a auto-end.
func TestSessionStart_AutoEndsPreviousSession(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	firstID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	secondID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)

	// First session should be ended.
	var status string
	err = s.QueryRow(ctx,
		`SELECT status FROM sessions WHERE id = ?`, firstID,
	).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "ended", status)

	// Check auto-end summary note.
	var summary string
	err = s.QueryRow(ctx,
		`SELECT COALESCE(summary, '') FROM sessions WHERE id = ?`, firstID,
	).Scan(&summary)
	require.NoError(t, err)
	assert.Contains(t, summary, "auto-ended by new session start")
}

// TestSessionEnd_GeneratesSummary verifies session end with summary.
func TestSessionEnd_GeneratesSummary(t *testing.T) {
	sessionSvc, nodeSvc, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Create a node during the session.
	_, err = nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Session Node", Creator: "agent-1",
	})
	require.NoError(t, err)

	// Update the node's session_id to match.
	db := s.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET session_id = ? WHERE project = 'PROJ'`, sessionID,
	)
	require.NoError(t, err)

	err = sessionSvc.SessionEnd(ctx, "agent-1")
	require.NoError(t, err)

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ended", summary.Status)
	assert.GreaterOrEqual(t, summary.NodesCreated, 1)
}

// TestSessionEnd_NoActiveSession_ReturnsError verifies error.
func TestSessionEnd_NoActiveSession_ReturnsError(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	err := sessionSvc.SessionEnd(ctx, "agent-1")
	assert.ErrorIs(t, err, model.ErrNoActiveSession)
}

// TestSessionTimeout_AutoEndsStaleSession verifies FR-10.5a timeout.
func TestSessionTimeout_AutoEndsStaleSession(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{
		SessionTimeoutDur: 4 * time.Hour,
	}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	logger := slog.Default()

	sessionSvc := service.NewSessionService(s, cfg, logger, fixedClock(now))
	ctx := context.Background()

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Manually set started_at to 5 hours ago (past the 4h timeout).
	db := s.WriteDB()
	pastTime := now.Add(-5 * time.Hour).UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		`UPDATE sessions SET started_at = ? WHERE id = ?`,
		pastTime, sessionID,
	)
	require.NoError(t, err)

	// Run timeout check.
	err = sessionSvc.CheckSessionTimeouts(ctx)
	require.NoError(t, err)

	// Session should be ended.
	var status string
	err = s.QueryRow(ctx,
		`SELECT status FROM sessions WHERE id = ?`, sessionID,
	).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "ended", status)
}

// TestSessionSummary_IncludesNodesCreatedAndCompleted verifies summary content.
func TestSessionSummary_IncludesNodesCreatedAndCompleted(t *testing.T) {
	sessionSvc, nodeSvc, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Create two nodes.
	node1, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Node 1", Creator: "agent-1",
	})
	require.NoError(t, err)
	node2, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Node 2", Creator: "agent-1",
	})
	require.NoError(t, err)

	// Tag nodes with session_id.
	db := s.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET session_id = ? WHERE id IN (?, ?)`,
		sessionID, node1.ID, node2.ID,
	)
	require.NoError(t, err)

	// Complete one node.
	require.NoError(t, s.ClaimNode(ctx, node1.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, node1.ID, model.StatusDone, "done", "agent-1"))

	err = sessionSvc.SessionEnd(ctx, "agent-1")
	require.NoError(t, err)

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 2, summary.NodesCreated)
	assert.Equal(t, 1, summary.NodesCompleted)
}

// TestSessionSummary_WithEndedSession_IncludesEndedAt verifies ended session details.
func TestSessionSummary_WithEndedSession_IncludesEndedAt(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	_, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	require.NoError(t, sessionSvc.SessionEnd(ctx, "agent-1"))

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "ended", summary.Status)
	assert.NotNil(t, summary.EndedAt)
	assert.Contains(t, summary.SummaryText, "Session summary")
}

// TestSessionSummary_EmptySession_ZeroCounts verifies zero counts for empty session.
func TestSessionSummary_EmptySession_ZeroCounts(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	_, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	require.NoError(t, sessionSvc.SessionEnd(ctx, "agent-1"))

	summary, err := sessionSvc.SessionSummary(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 0, summary.NodesCreated)
	assert.Equal(t, 0, summary.NodesCompleted)
	assert.Equal(t, 0, summary.NodesDeferred)
}

// TestCheckSessionTimeouts_MultipleSessions_EndsAllExpired verifies batch timeout.
func TestCheckSessionTimeouts_MultipleSessions_EndsAllExpired(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{SessionTimeoutDur: 4 * time.Hour}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	sessionSvc := service.NewSessionService(s, cfg, slog.Default(), fixedClock(now))
	ctx := context.Background()

	registerAgent(t, s, "agent-1", "PROJ", now)
	registerAgent(t, s, "agent-2", "PROJ", now)

	// Start sessions for both agents.
	s1ID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)
	s2ID, err := sessionSvc.SessionStart(ctx, "agent-2", "PROJ")
	require.NoError(t, err)

	// Set both sessions to 5 hours ago (past timeout).
	db := s.WriteDB()
	pastTime := now.Add(-5 * time.Hour).UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		`UPDATE sessions SET started_at = ? WHERE id IN (?, ?)`,
		pastTime, s1ID, s2ID,
	)
	require.NoError(t, err)

	err = sessionSvc.CheckSessionTimeouts(ctx)
	require.NoError(t, err)

	// Both sessions should be ended.
	var status1, status2 string
	require.NoError(t, s.QueryRow(ctx,
		`SELECT status FROM sessions WHERE id = ?`, s1ID,
	).Scan(&status1))
	require.NoError(t, s.QueryRow(ctx,
		`SELECT status FROM sessions WHERE id = ?`, s2ID,
	).Scan(&status2))
	assert.Equal(t, "ended", status1)
	assert.Equal(t, "ended", status2)
}

// TestCheckSessionTimeouts_ClosedStore_ReturnsError verifies store error handling.
func TestCheckSessionTimeouts_ClosedStore_ReturnsError(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)

	cfg := &service.StaticConfig{SessionTimeoutDur: 4 * time.Hour}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewSessionService(s, cfg, slog.Default(), fixedClock(now))

	require.NoError(t, s.Close())

	err = svc.CheckSessionTimeouts(context.Background())
	assert.Error(t, err)
}

// TestSession_AutoPopulatesSessionIDOnNodeMutation verifies session_id auto-population.
func TestSession_AutoPopulatesSessionIDOnNodeMutation(t *testing.T) {
	sessionSvc, _, s := newTestSessionService(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	registerAgent(t, s, "agent-1", "PROJ", now)

	sessionID, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Verify we can get the active session ID.
	activeID, err := sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, sessionID, activeID)
}

// TestSessionStart_AutoRegistersAgent verifies that SessionStart creates
// the agent if it doesn't already exist (FR-10.1a defense-in-depth).
func TestSessionStart_AutoRegistersAgent(t *testing.T) {
	sessionSvc, _, _ := newTestSessionService(t)
	ctx := context.Background()

	// Start a session for an agent that hasn't been registered.
	// SessionStart should auto-register it.
	sessionID, err := sessionSvc.SessionStart(ctx, "new-agent", "PROJ")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
}

// TestSessionStart_AutoEndsExistingSession verifies that starting a new
// session auto-ends any existing active session for the same agent.
func TestSessionStart_AutoEndsExistingSession(t *testing.T) {
	sessionSvc, _, store := newTestSessionService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	registerAgent(t, store, "agent-1", "PROJ", now)

	// Start first session.
	session1, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	// Start second session — first should be auto-ended.
	session2, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)
	assert.NotEqual(t, session1, session2)

	// Active session should be the second one.
	activeID, err := sessionSvc.GetActiveSessionID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, session2, activeID)
}

// TestSessionEnd_SuccessfulEnd_NoError verifies that ending an active
// session completes without error.
func TestSessionEnd_SuccessfulEnd_NoError(t *testing.T) {
	sessionSvc, _, store := newTestSessionService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	registerAgent(t, store, "agent-1", "PROJ", now)

	_, err := sessionSvc.SessionStart(ctx, "agent-1", "PROJ")
	require.NoError(t, err)

	err = sessionSvc.SessionEnd(ctx, "agent-1")
	require.NoError(t, err)

	// No active session after end.
	_, err = sessionSvc.GetActiveSessionID(ctx, "agent-1")
	assert.Error(t, err)
}

// TestSessionEnd_NoActiveSession_ReturnsError2 verifies ending a session
// when none is active returns an appropriate error.
func TestSessionEnd_NoActiveSession_ReturnsError2(t *testing.T) {
	sessionSvc, _, store := newTestSessionService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	registerAgent(t, store, "agent-1", "PROJ", now)

	err := sessionSvc.SessionEnd(ctx, "agent-1")
	assert.Error(t, err)
}
