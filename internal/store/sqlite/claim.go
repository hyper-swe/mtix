// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ClaimNode atomically claims a node for an agent per FR-10.4.
// Sets assignee and transitions to in_progress via compare-and-swap.
//
// Returns ErrAlreadyClaimed if the node is already in_progress.
// Returns ErrNodeBlocked if the node is blocked.
// Returns ErrStillDeferred if the node is deferred with future defer_until.
// Returns ErrInvalidTransition for other invalid states.
func (s *Store) ClaimNode(ctx context.Context, id, agentID string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := s.validateClaimStatus(ctx, tx, id); err != nil {
			return err
		}

		now := s.clock()
		nowStr := now.Format(time.RFC3339)

		// Atomic update: set status, assignee, and agent_state.
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?,
			 updated_at = ?, defer_until = NULL
			 WHERE id = ? AND deleted_at IS NULL`,
			string(model.StatusInProgress), agentID, string(model.AgentStateWorking),
			nowStr, id,
		)
		if err != nil {
			return fmt.Errorf("claim node %s: %w", id, err)
		}

		if err := ensureAndSyncAgent(ctx, tx, agentID, id, nowStr); err != nil {
			return err
		}

		// Record claim activity.
		return appendActivityEntry(ctx, tx, id, model.ActivityEntry{
			ID:        fmt.Sprintf("act-%d", now.UnixNano()),
			Type:      model.ActivityTypeClaim,
			Author:    agentID,
			Text:      fmt.Sprintf("Claimed by %s", agentID),
			CreatedAt: now,
		})
	})
}

// validateClaimStatus reads the node and checks it is in a claimable state.
func (s *Store) validateClaimStatus(ctx context.Context, tx *sql.Tx, id string) error {
	var currentStatus string
	var deferUntil sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT status, defer_until FROM nodes
		 WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&currentStatus, &deferUntil)
	if err == sql.ErrNoRows {
		return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read node %s for claim: %w", id, err)
	}

	switch model.Status(currentStatus) {
	case model.StatusOpen:
		return nil
	case model.StatusDeferred:
		return s.checkDeferExpired(id, deferUntil)
	case model.StatusInProgress:
		return fmt.Errorf("node %s: %w", id, model.ErrAlreadyClaimed)
	case model.StatusBlocked:
		return fmt.Errorf("node %s: %w", id, model.ErrNodeBlocked)
	default:
		return fmt.Errorf("cannot claim node %s in %s status: %w",
			id, currentStatus, model.ErrInvalidTransition)
	}
}

// checkDeferExpired returns nil if the defer period has expired, or
// ErrStillDeferred if the node cannot be claimed yet per FR-10.4.
func (s *Store) checkDeferExpired(id string, deferUntil sql.NullString) error {
	if !deferUntil.Valid || deferUntil.String == "" {
		return nil
	}
	until, parseErr := time.Parse(time.RFC3339, deferUntil.String)
	if parseErr != nil {
		return nil // Unparsable defer_until — allow claim.
	}
	if until.After(s.clock()) {
		return fmt.Errorf("node %s deferred until %s: %w",
			id, deferUntil.String, model.ErrStillDeferred)
	}
	return nil
}

// UnclaimNode releases a node assignment per FR-10.4.
// Requires a mandatory reason. Sets status back to open.
func (s *Store) UnclaimNode(ctx context.Context, id, reason, author string) error {
	if reason == "" {
		return fmt.Errorf("unclaim reason is required: %w", model.ErrInvalidInput)
	}

	return s.WithTx(ctx, func(tx *sql.Tx) error {
		var currentStatus string
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`,
			id,
		).Scan(&currentStatus)
		if err == sql.ErrNoRows {
			return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("read node %s for unclaim: %w", id, err)
		}

		if model.Status(currentStatus) != model.StatusInProgress {
			return fmt.Errorf(
				"cannot unclaim node %s in %s status: %w",
				id, currentStatus, model.ErrInvalidTransition,
			)
		}

		now := s.clock()
		nowStr := now.Format(time.RFC3339)

		// Get the current assignee before clearing.
		var assignee sql.NullString
		_ = tx.QueryRowContext(ctx,
			`SELECT assignee FROM nodes WHERE id = ? AND deleted_at IS NULL`, id,
		).Scan(&assignee)

		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET status = ?, assignee = NULL, agent_state = NULL,
			 updated_at = ?
			 WHERE id = ? AND deleted_at IS NULL`,
			string(model.StatusOpen), nowStr, id,
		)
		if err != nil {
			return fmt.Errorf("unclaim node %s: %w", id, err)
		}

		// FR-10.1b: Reset agent state on unclaim.
		if assignee.Valid && assignee.String != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE agents SET current_node_id = NULL, state = 'idle', state_changed_at = ?
				 WHERE agent_id = ?`,
				nowStr, assignee.String,
			); err != nil {
				return fmt.Errorf("reset agent %s on unclaim: %w", assignee.String, err)
			}
		}

		return appendActivityEntry(ctx, tx, id, model.ActivityEntry{
			ID:        fmt.Sprintf("act-%d", now.UnixNano()),
			Type:      model.ActivityTypeUnclaim,
			Author:    author,
			Text:      reason,
			CreatedAt: now,
		})
	})
}

// ForceReclaimNode reclaims a node from a stale agent per FR-10.4a.
// Succeeds only if the current assignee's last heartbeat exceeds staleThreshold.
//
// Returns ErrAgentStillActive if the current agent is not stale.
func (s *Store) ForceReclaimNode(ctx context.Context, id, agentID string, staleThreshold time.Duration) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		currentAssignee, err := s.validateForceReclaim(ctx, tx, id, staleThreshold)
		if err != nil {
			return err
		}

		now := s.clock()
		nowStr := now.Format(time.RFC3339)

		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET assignee = ?, agent_state = ?, updated_at = ?
			 WHERE id = ? AND deleted_at IS NULL`,
			agentID, string(model.AgentStateWorking), nowStr, id,
		)
		if err != nil {
			return fmt.Errorf("force-reclaim node %s: %w", id, err)
		}

		if err := resetOldAgent(ctx, tx, currentAssignee, nowStr); err != nil {
			return err
		}
		if err := ensureAndSyncAgent(ctx, tx, agentID, id, nowStr); err != nil {
			return err
		}

		return appendActivityEntry(ctx, tx, id, model.ActivityEntry{
			ID:        fmt.Sprintf("act-%d", now.UnixNano()),
			Type:      model.ActivityTypeClaim,
			Author:    agentID,
			Text:      fmt.Sprintf("Force-reclaimed from %s", currentAssignee),
			CreatedAt: now,
		})
	})
}

// validateForceReclaim checks that a node can be force-reclaimed: it must be
// in_progress and the current assignee must be stale. Returns the current
// assignee string on success.
func (s *Store) validateForceReclaim(
	ctx context.Context, tx *sql.Tx, id string, staleThreshold time.Duration,
) (string, error) {
	var currentAssignee sql.NullString
	var currentStatus string

	err := tx.QueryRowContext(ctx,
		`SELECT status, assignee FROM nodes
		 WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&currentStatus, &currentAssignee)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read node %s for force-reclaim: %w", id, err)
	}

	if model.Status(currentStatus) != model.StatusInProgress {
		return "", fmt.Errorf(
			"cannot force-reclaim node %s in %s status: %w",
			id, currentStatus, model.ErrInvalidTransition,
		)
	}

	if err := s.checkAgentStale(ctx, tx, currentAssignee, staleThreshold); err != nil {
		return "", err
	}

	return currentAssignee.String, nil
}

// checkAgentStale verifies the current assignee's heartbeat exceeds the
// stale threshold. Returns ErrAgentStillActive if the agent is still active.
func (s *Store) checkAgentStale(
	ctx context.Context, tx *sql.Tx, assignee sql.NullString, threshold time.Duration,
) error {
	if !assignee.Valid || assignee.String == "" {
		return nil // No assignee — treat as stale.
	}

	var lastHeartbeat sql.NullString
	hbErr := tx.QueryRowContext(ctx,
		`SELECT last_heartbeat FROM agents WHERE agent_id = ?`,
		assignee.String,
	).Scan(&lastHeartbeat)

	if hbErr != nil || !lastHeartbeat.Valid {
		return nil // Agent not in table or no heartbeat — treat as stale.
	}

	hb, parseErr := time.Parse(time.RFC3339, lastHeartbeat.String)
	if parseErr != nil {
		return nil // Unparsable heartbeat — treat as stale.
	}

	elapsed := s.clock().Sub(hb)
	if elapsed < threshold {
		return fmt.Errorf(
			"agent %s is still active (heartbeat %s ago): %w",
			assignee.String, elapsed.Truncate(time.Second), model.ErrAgentStillActive,
		)
	}

	return nil
}

// resetOldAgent clears the old agent's state on force-reclaim per FR-10.1b.
func resetOldAgent(ctx context.Context, tx *sql.Tx, agentID, nowStr string) error {
	if agentID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET current_node_id = NULL, state = 'idle', state_changed_at = ?
		 WHERE agent_id = ?`,
		nowStr, agentID,
	); err != nil {
		return fmt.Errorf("reset agent %s on force-reclaim: %w", agentID, err)
	}
	return nil
}

// ensureAndSyncAgent auto-registers a new agent if not exists and syncs its
// state with the claimed node per FR-10.1a/FR-10.1b.
func ensureAndSyncAgent(ctx context.Context, tx *sql.Tx, agentID, nodeID, nowStr string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, (SELECT project FROM nodes WHERE id = ?), 'idle', ?, ?)`,
		agentID, nodeID, nowStr, nowStr,
	); err != nil {
		return fmt.Errorf("ensure agent %s: %w", agentID, err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET current_node_id = ?, state = 'working', state_changed_at = ?
		 WHERE agent_id = ?`,
		nodeID, nowStr, agentID,
	); err != nil {
		return fmt.Errorf("sync agent %s state: %w", agentID, err)
	}

	return nil
}
