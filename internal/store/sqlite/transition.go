// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// TransitionStatus changes a node's status per FR-3.5 state machine rules.
// Validates the transition, records a status_change activity entry,
// sets closed_at on done/canceled, clears it on reopen.
// Handles idempotent transitions per FR-7.7a.
//
// Returns ErrInvalidTransition if the transition is not allowed.
// Returns ErrNotFound if the node does not exist or is soft-deleted.
func (s *Store) TransitionStatus(ctx context.Context, id string, toStatus model.Status, reason, author string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return executeTransitionTx(ctx, tx, id, toStatus, reason, author)
	})
}

// executeTransitionTx performs the status transition within a transaction.
func executeTransitionTx(ctx context.Context, tx *sql.Tx, id string, toStatus model.Status, reason, author string) error {
	fromStatus, parentID, err := readNodeStatus(ctx, tx, id)
	if err != nil {
		return err
	}

	// FR-7.7a: Idempotent transition — already in target state.
	if fromStatus == toStatus {
		return nil
	}

	if validErr := model.ValidateTransition(fromStatus, toStatus); validErr != nil {
		return validErr
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	setClauses, args := buildTransitionClauses(fromStatus, toStatus, nowStr)
	query := "UPDATE nodes SET " + setClauses + " WHERE id = ? AND deleted_at IS NULL"
	args = append(args, id)

	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("update status for %s: %w", id, err)
	}

	if err := appendActivityEntry(ctx, tx, id, model.ActivityEntry{
		ID:        fmt.Sprintf("act-%d", now.UnixNano()),
		Type:      model.ActivityTypeStatusChange,
		Author:    author,
		Text:      reason,
		CreatedAt: now,
		Metadata:  mustMarshal(map[string]string{"from_status": string(fromStatus), "to_status": string(toStatus)}),
	}); err != nil {
		return fmt.Errorf("record activity for %s: %w", id, err)
	}

	if parentID != "" {
		if err := recalculateProgress(ctx, tx, parentID); err != nil {
			return fmt.Errorf("recalculate progress after transition: %w", err)
		}
	}

	return nil
}

// readNodeStatus reads the current status and parent ID for transition validation.
func readNodeStatus(ctx context.Context, tx *sql.Tx, id string) (model.Status, string, error) {
	var currentStatus, parentID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT status, parent_id FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&currentStatus, &parentID)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return "", "", fmt.Errorf("read status for %s: %w", id, err)
	}
	return model.Status(currentStatus.String), parentID.String, nil
}

// buildTransitionClauses builds the SET clause and args for a status transition.
func buildTransitionClauses(fromStatus, toStatus model.Status, nowStr string) (string, []any) {
	setClauses := "status = ?, updated_at = ?"
	args := []any{string(toStatus), nowStr}

	if toStatus == model.StatusDone || toStatus == model.StatusCancelled {
		setClauses += ", closed_at = ?"
		args = append(args, nowStr)
	}

	if (fromStatus == model.StatusDone || fromStatus == model.StatusCancelled) &&
		(toStatus == model.StatusOpen) {
		setClauses += ", closed_at = NULL"
	}

	if toStatus == model.StatusBlocked || toStatus == model.StatusInvalidated {
		setClauses += ", previous_status = ?"
		args = append(args, string(fromStatus))
	}

	if toStatus == model.StatusDone {
		setClauses += ", progress = 1.0"
	}

	return setClauses, args
}

// appendActivityEntry appends an activity entry to a node's activity JSON array.
func appendActivityEntry(ctx context.Context, tx *sql.Tx, nodeID string, entry model.ActivityEntry) error {
	// Read current activity JSON.
	var activityJSON sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT activity FROM nodes WHERE id = ?`,
		nodeID,
	).Scan(&activityJSON)
	if err != nil {
		return fmt.Errorf("read activity for %s: %w", nodeID, err)
	}

	var entries []model.ActivityEntry
	if activityJSON.Valid && activityJSON.String != "" && activityJSON.String != "[]" {
		if unmarshalErr := json.Unmarshal([]byte(activityJSON.String), &entries); unmarshalErr != nil {
			return fmt.Errorf("unmarshal activity for %s: %w", nodeID, unmarshalErr)
		}
	}

	entries = append(entries, entry)

	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal activity for %s: %w", nodeID, err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET activity = ? WHERE id = ?`,
		string(data), nodeID,
	)
	if err != nil {
		return fmt.Errorf("update activity for %s: %w", nodeID, err)
	}

	return nil
}

// mustMarshal marshals v to JSON, panicking on error (only for static data).
func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return data
}
