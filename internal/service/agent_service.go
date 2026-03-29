// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// agentTransitions defines valid agent state transitions per FR-10.2.
// idle → working → stuck|done.
var agentTransitions = map[model.AgentState][]model.AgentState{
	model.AgentStateIdle:    {model.AgentStateWorking},
	model.AgentStateWorking: {model.AgentStateStuck, model.AgentStateDone, model.AgentStateIdle},
	model.AgentStateStuck:   {model.AgentStateWorking, model.AgentStateIdle},
	model.AgentStateDone:    {model.AgentStateIdle},
}

// AgentService manages agent lifecycle per FR-10.1–10.4a.
type AgentService struct {
	store       store.Store
	broadcaster EventBroadcaster
	config      ConfigProvider
	logger      *slog.Logger
	clock       func() time.Time
}

// NewAgentService creates an AgentService with required dependencies.
func NewAgentService(
	s store.Store,
	broadcaster EventBroadcaster,
	config ConfigProvider,
	logger *slog.Logger,
	clock func() time.Time,
) *AgentService {
	if broadcaster == nil {
		broadcaster = &NoopBroadcaster{}
	}
	if config == nil {
		config = &StaticConfig{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentService{
		store:       s,
		broadcaster: broadcaster,
		config:      config,
		logger:      logger,
		clock:       clock,
	}
}

// RegisterAgent creates a new agent record per FR-10.1a.
// Sets initial state to idle with timestamps from the injected clock.
// Returns ErrAlreadyExists if an agent with the same ID already exists.
func (svc *AgentService) RegisterAgent(ctx context.Context, agentID, project string) error {
	if agentID == "" {
		return fmt.Errorf("agent ID is required: %w", model.ErrInvalidInput)
	}
	if project == "" {
		return fmt.Errorf("project is required: %w", model.ErrInvalidInput)
	}

	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()
	result, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, ?, 'idle', ?, ?)`,
		agentID, project, now, now,
	)
	if err != nil {
		return fmt.Errorf("register agent %s: %w", agentID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check register result for %s: %w", agentID, err)
	}
	if rows == 0 {
		return fmt.Errorf("agent %s: %w", agentID, model.ErrAlreadyExists)
	}

	return nil
}

// EnsureAgent creates the agent if it does not exist (idempotent) per FR-10.1a.
// Used by claim and session start for defensive auto-registration.
// Does NOT overwrite existing agent state.
func (svc *AgentService) EnsureAgent(ctx context.Context, agentID, project string) error {
	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, ?, 'idle', ?, ?)`,
		agentID, project, now, now,
	)
	if err != nil {
		return fmt.Errorf("ensure agent %s: %w", agentID, err)
	}
	return nil
}

// UpdateAgentState validates and applies an agent state transition per FR-10.2.
// Broadcasts agent.state event and agent.stuck event if transitioning to stuck.
func (svc *AgentService) UpdateAgentState(
	ctx context.Context, agentID string, newState model.AgentState,
) error {
	currentState, err := svc.GetAgentState(ctx, agentID)
	if err != nil {
		return fmt.Errorf("get agent %s state: %w", agentID, err)
	}

	if !isValidAgentTransition(currentState, newState) {
		return fmt.Errorf(
			"invalid agent transition %s → %s for %s: %w",
			currentState, newState, agentID, model.ErrInvalidTransition,
		)
	}

	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET state = ?, state_changed_at = ? WHERE agent_id = ?`,
		string(newState), now, agentID,
	)
	if err != nil {
		return fmt.Errorf("update agent %s state: %w", agentID, err)
	}

	// Broadcast state change event.
	svc.broadcastAgentEvent(ctx, EventAgentStateChanged, agentID, nil)

	// FR-10.3a: If transitioning to stuck, broadcast agent.stuck event.
	if newState == model.AgentStateStuck {
		svc.broadcastAgentEvent(ctx, EventAgentStuck, agentID, nil)
	}

	return nil
}

// GetAgentState returns the current state of an agent.
func (svc *AgentService) GetAgentState(ctx context.Context, agentID string) (model.AgentState, error) {
	var state string
	err := svc.store.QueryRow(ctx,
		`SELECT state FROM agents WHERE agent_id = ?`, agentID,
	).Scan(&state)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("agent %s: %w", agentID, model.ErrNotFound)
		}
		return "", fmt.Errorf("get agent %s state: %w", agentID, err)
	}
	return model.AgentState(state), nil
}

// Heartbeat updates an agent's last_heartbeat timestamp per FR-10.3.
// Returns ErrNotFound if the agent does not exist (FR-10.1a: no phantom agents).
func (svc *AgentService) Heartbeat(ctx context.Context, agentID string) error {
	now := svc.clock().UTC().Format(time.RFC3339)
	db := svc.store.WriteDB()
	result, err := db.ExecContext(ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		now, agentID,
	)
	if err != nil {
		return fmt.Errorf("heartbeat agent %s: %w", agentID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check heartbeat result for %s: %w", agentID, err)
	}
	if rows == 0 {
		return fmt.Errorf("agent %s: %w", agentID, model.ErrNotFound)
	}
	return nil
}

// GetLastHeartbeat returns the last heartbeat timestamp for an agent.
func (svc *AgentService) GetLastHeartbeat(ctx context.Context, agentID string) (time.Time, error) {
	var hbStr string
	err := svc.store.QueryRow(ctx,
		`SELECT last_heartbeat FROM agents WHERE agent_id = ?`, agentID,
	).Scan(&hbStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("get heartbeat for %s: %w", agentID, err)
	}

	t, err := time.Parse(time.RFC3339, hbStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse heartbeat for %s: %w", agentID, err)
	}
	return t, nil
}

// GetStaleAgents returns agent IDs whose last_heartbeat exceeds threshold per FR-10.3.
func (svc *AgentService) GetStaleAgents(
	ctx context.Context, threshold time.Duration,
) ([]string, error) {
	cutoff := svc.clock().Add(-threshold).UTC().Format(time.RFC3339)

	rows, err := svc.store.Query(ctx,
		`SELECT agent_id FROM agents WHERE last_heartbeat < ?`, cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("query stale agents: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			svc.logger.Error("close stale agents rows", "error", closeErr)
		}
	}()

	var agentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stale agent: %w", err)
		}
		agentIDs = append(agentIDs, id)
	}
	return agentIDs, rows.Err()
}

// GetCurrentWork returns the node currently claimed by the agent per FR-10.4.
func (svc *AgentService) GetCurrentWork(ctx context.Context, agentID string) (*model.Node, error) {
	var nodeID sql.NullString
	err := svc.store.QueryRow(ctx,
		`SELECT current_node_id FROM agents WHERE agent_id = ?`, agentID,
	).Scan(&nodeID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("agent %s: %w", agentID, model.ErrNotFound)
		}
		return nil, fmt.Errorf("get current work for %s: %w", agentID, err)
	}

	if !nodeID.Valid || nodeID.String == "" {
		return nil, fmt.Errorf("agent %s has no current work: %w", agentID, model.ErrNotFound)
	}

	return svc.store.GetNode(ctx, nodeID.String)
}

// CheckStuckTimeouts checks for stuck agents past their timeout and auto-unclaims per FR-10.3a.
func (svc *AgentService) CheckStuckTimeouts(ctx context.Context) error {
	timeout := svc.config.AgentStuckTimeout()
	if timeout == 0 {
		return nil // Auto-unclaim not configured.
	}

	cutoff := svc.clock().Add(-timeout).UTC().Format(time.RFC3339)

	rows, err := svc.store.Query(ctx,
		`SELECT agent_id, current_node_id FROM agents
		 WHERE state = 'stuck' AND state_changed_at < ? AND current_node_id IS NOT NULL`,
		cutoff,
	)
	if err != nil {
		return fmt.Errorf("query stuck agents: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			svc.logger.Error("close stuck agents rows", "error", closeErr)
		}
	}()

	type stuckAgent struct {
		agentID string
		nodeID  string
	}
	var agents []stuckAgent

	for rows.Next() {
		var sa stuckAgent
		if err := rows.Scan(&sa.agentID, &sa.nodeID); err != nil {
			return fmt.Errorf("scan stuck agent: %w", err)
		}
		agents = append(agents, sa)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stuck agents: %w", err)
	}

	for _, sa := range agents {
		svc.logger.Info("auto-unclaiming stuck agent",
			"agent_id", sa.agentID, "node_id", sa.nodeID)

		if err := svc.store.UnclaimNode(ctx, sa.nodeID, "auto-unclaimed: stuck timeout", sa.agentID); err != nil {
			svc.logger.Error("failed to auto-unclaim",
				"agent_id", sa.agentID, "node_id", sa.nodeID, "error", err)
			continue
		}

		// Reset agent state.
		db := svc.store.WriteDB()
		now := svc.clock().UTC().Format(time.RFC3339)
		if _, err := db.ExecContext(ctx,
			`UPDATE agents SET state = 'idle', current_node_id = NULL, state_changed_at = ? WHERE agent_id = ?`,
			now, sa.agentID,
		); err != nil {
			svc.logger.Error("failed to reset stuck agent",
				"agent_id", sa.agentID, "error", err)
		}

		svc.broadcastAgentEvent(ctx, EventNodeUnclaimed, sa.nodeID, nil)
	}

	return nil
}

// isValidAgentTransition checks if a state transition is allowed per FR-10.2.
func isValidAgentTransition(from, to model.AgentState) bool {
	valid, ok := agentTransitions[from]
	if !ok {
		return false
	}
	for _, s := range valid {
		if s == to {
			return true
		}
	}
	return false
}

// broadcastAgentEvent is a helper that broadcasts an agent-related event.
func (svc *AgentService) broadcastAgentEvent(
	ctx context.Context, eventType EventType, subjectID string, data json.RawMessage,
) {
	event := Event{
		Type:      eventType,
		NodeID:    subjectID,
		Timestamp: svc.clock(),
		Data:      data,
	}
	if err := svc.broadcaster.Broadcast(ctx, event); err != nil {
		svc.logger.Error("failed to broadcast agent event",
			"type", eventType, "subject", subjectID, "error", err)
	}
}
