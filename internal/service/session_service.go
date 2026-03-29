// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// SessionSummary contains the summary data for a session per FR-10.5a.
type SessionSummary struct {
	// SessionID is the ULID of the session.
	SessionID string `json:"session_id"`

	// Status is "active" or "ended".
	Status string `json:"status"`

	// StartedAt is when the session began.
	StartedAt time.Time `json:"started_at"`

	// EndedAt is when the session ended (nil if active).
	EndedAt *time.Time `json:"ended_at,omitempty"`

	// NodesCreated is the count of nodes created during the session.
	NodesCreated int `json:"nodes_created"`

	// NodesCompleted is the count of nodes completed during the session.
	NodesCompleted int `json:"nodes_completed"`

	// NodesDeferred is the count of nodes deferred during the session.
	NodesDeferred int `json:"nodes_deferred"`

	// SummaryText is a human-readable summary.
	SummaryText string `json:"summary_text,omitempty"`
}

// SessionService manages agent session lifecycle per FR-10.5a.
type SessionService struct {
	store  store.Store
	config ConfigProvider
	logger *slog.Logger
	clock  func() time.Time
}

// NewSessionService creates a SessionService with required dependencies.
func NewSessionService(
	s store.Store,
	config ConfigProvider,
	logger *slog.Logger,
	clock func() time.Time,
) *SessionService {
	if config == nil {
		config = &StaticConfig{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionService{
		store:  s,
		config: config,
		logger: logger,
		clock:  clock,
	}
}

// SessionStart creates a new active session for an agent per FR-10.5a.
// If an active session exists, it is auto-ended with a summary note.
// Auto-registers the agent if it does not exist per FR-10.1a.
// Returns the new session's ULID.
func (svc *SessionService) SessionStart(ctx context.Context, agentID, project string) (string, error) {
	// FR-10.1a: Auto-register agent if not exists (defense-in-depth).
	db := svc.store.WriteDB()
	now := svc.clock()
	nowStr := now.UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, ?, 'idle', ?, ?)`,
		agentID, project, nowStr, nowStr,
	); err != nil {
		return "", fmt.Errorf("ensure agent %s for session: %w", agentID, err)
	}

	// Auto-end any existing active session.
	if err := svc.autoEndActiveSession(ctx, agentID); err != nil {
		return "", fmt.Errorf("auto-end session for %s: %w", agentID, err)
	}

	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate session ULID: %w", err)
	}

	sessionID := id.String()
	startedAt := now.UTC().Format(time.RFC3339)

	_, err = db.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, project, started_at, status)
		 VALUES (?, ?, ?, ?, 'active')`,
		sessionID, agentID, project, startedAt,
	)
	if err != nil {
		return "", fmt.Errorf("create session for %s: %w", agentID, err)
	}

	return sessionID, nil
}

// SessionEnd ends the active session for an agent and generates a summary per FR-10.5a.
// Returns ErrNoActiveSession if no active session exists.
func (svc *SessionService) SessionEnd(ctx context.Context, agentID string) error {
	sessionID, err := svc.GetActiveSessionID(ctx, agentID)
	if err != nil {
		return err
	}

	summary, err := svc.buildSessionSummary(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("build session summary: %w", err)
	}

	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE sessions SET status = 'ended', ended_at = ?, summary = ? WHERE id = ?`,
		now, summary, sessionID,
	)
	if err != nil {
		return fmt.Errorf("end session %s: %w", sessionID, err)
	}

	return nil
}

// SessionSummary returns the summary for the most recent session of an agent.
func (svc *SessionService) SessionSummary(ctx context.Context, agentID string) (*SessionSummary, error) {
	var (
		sessionID, status, startedAtStr string
		endedAt, summaryText            sql.NullString
	)

	err := svc.store.QueryRow(ctx,
		`SELECT id, status, started_at, ended_at, COALESCE(summary, '')
		 FROM sessions WHERE agent_id = ?
		 ORDER BY started_at DESC LIMIT 1`,
		agentID,
	).Scan(&sessionID, &status, &startedAtStr, &endedAt, &summaryText)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no session for %s: %w", agentID, model.ErrNotFound)
		}
		return nil, fmt.Errorf("query session for %s: %w", agentID, err)
	}

	startedAt, _ := time.Parse(time.RFC3339, startedAtStr)

	result := &SessionSummary{
		SessionID:   sessionID,
		Status:      status,
		StartedAt:   startedAt,
		SummaryText: summaryText.String,
	}

	if endedAt.Valid {
		t, _ := time.Parse(time.RFC3339, endedAt.String)
		result.EndedAt = &t
	}

	// Count nodes for this session.
	counts, err := svc.countSessionNodes(ctx, sessionID)
	if err == nil {
		result.NodesCreated = counts.created
		result.NodesCompleted = counts.completed
		result.NodesDeferred = counts.deferred
	}

	return result, nil
}

// GetActiveSessionID returns the active session ID for an agent.
// Returns ErrNoActiveSession if no active session exists.
func (svc *SessionService) GetActiveSessionID(ctx context.Context, agentID string) (string, error) {
	var sessionID string
	err := svc.store.QueryRow(ctx,
		`SELECT id FROM sessions WHERE agent_id = ? AND status = 'active' LIMIT 1`,
		agentID,
	).Scan(&sessionID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no active session for %s: %w", agentID, model.ErrNoActiveSession)
		}
		return "", fmt.Errorf("query active session for %s: %w", agentID, err)
	}
	return sessionID, nil
}

// CheckSessionTimeouts ends sessions that exceed the configured timeout per FR-10.5a.
func (svc *SessionService) CheckSessionTimeouts(ctx context.Context) error {
	timeout := svc.config.SessionTimeout()
	cutoff := svc.clock().Add(-timeout).UTC().Format(time.RFC3339)

	rows, err := svc.store.Query(ctx,
		`SELECT id, agent_id FROM sessions
		 WHERE status = 'active' AND started_at < ?`,
		cutoff,
	)
	if err != nil {
		return fmt.Errorf("query timed-out sessions: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			svc.logger.Error("close timeout sessions rows", "error", closeErr)
		}
	}()

	type timedOutSession struct {
		id      string
		agentID string
	}
	var sessions []timedOutSession

	for rows.Next() {
		var s timedOutSession
		if err := rows.Scan(&s.id, &s.agentID); err != nil {
			return fmt.Errorf("scan timed-out session: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate timed-out sessions: %w", err)
	}

	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()

	for _, sess := range sessions {
		svc.logger.Info("auto-ending timed-out session",
			"session_id", sess.id, "agent_id", sess.agentID)

		if _, err := db.ExecContext(ctx,
			`UPDATE sessions SET status = 'ended', ended_at = ?, summary = ?
			 WHERE id = ?`,
			now, "auto-ended due to timeout", sess.id,
		); err != nil {
			svc.logger.Error("failed to auto-end session",
				"session_id", sess.id, "error", err)
		}
	}

	return nil
}

// autoEndActiveSession ends any active session for the agent.
func (svc *SessionService) autoEndActiveSession(ctx context.Context, agentID string) error {
	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()

	_, err := db.ExecContext(ctx,
		`UPDATE sessions SET status = 'ended', ended_at = ?, summary = ?
		 WHERE agent_id = ? AND status = 'active'`,
		now, "auto-ended by new session start", agentID,
	)
	if err != nil {
		return fmt.Errorf("auto-end active session for %s: %w", agentID, err)
	}
	return nil
}

// buildSessionSummary generates a text summary for a session.
func (svc *SessionService) buildSessionSummary(ctx context.Context, sessionID string) (string, error) {
	counts, err := svc.countSessionNodes(ctx, sessionID)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"Session summary: %d nodes created, %d completed, %d deferred",
		counts.created, counts.completed, counts.deferred,
	), nil
}

type sessionNodeCounts struct {
	created   int
	completed int
	deferred  int
}

// countSessionNodes counts nodes associated with a session by status.
func (svc *SessionService) countSessionNodes(ctx context.Context, sessionID string) (sessionNodeCounts, error) {
	var counts sessionNodeCounts

	// Count total nodes created in session.
	err := svc.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE session_id = ?`, sessionID,
	).Scan(&counts.created)
	if err != nil {
		return counts, fmt.Errorf("count session nodes: %w", err)
	}

	// Count completed nodes.
	err = svc.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE session_id = ? AND status = 'done'`, sessionID,
	).Scan(&counts.completed)
	if err != nil {
		return counts, fmt.Errorf("count completed session nodes: %w", err)
	}

	// Count deferred nodes.
	err = svc.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE session_id = ? AND status = 'deferred'`, sessionID,
	).Scan(&counts.deferred)
	if err != nil {
		return counts, fmt.Errorf("count deferred session nodes: %w", err)
	}

	return counts, nil
}
