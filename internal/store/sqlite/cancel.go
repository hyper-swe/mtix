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

// CancelNode cancels a node with mandatory reason per FR-6.3.
// If cascade is true, all descendants are also canceled.
// Recalculates progress excluding canceled nodes from denominator (FR-5.4).
//
// Returns ErrInvalidInput if reason is empty.
// Returns ErrNotFound if the node does not exist.
// Returns ErrInvalidTransition if the transition is not allowed.
func (s *Store) CancelNode(ctx context.Context, id, reason, author string, cascade bool) error {
	if reason == "" {
		return fmt.Errorf("cancel reason is required: %w", model.ErrInvalidInput)
	}

	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return executeCancelTx(ctx, tx, id, reason, author, cascade)
	})
}

// executeCancelTx performs the cancel operation within a transaction.
func executeCancelTx(ctx context.Context, tx *sql.Tx, id, reason, author string, cascade bool) error {
	fromStatus, parentID, err := readNodeForCancel(ctx, tx, id)
	if err != nil {
		return err
	}

	if validErr := model.ValidateTransition(fromStatus, model.StatusCancelled); validErr != nil {
		return validErr
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	if err := applyCancelUpdate(ctx, tx, id, fromStatus, reason, author, now, nowStr); err != nil {
		return err
	}

	payload, _ := model.EncodePayload(&model.TransitionStatusPayload{
		From:   fromStatus,
		To:     model.StatusCancelled,
		Reason: reason,
	})
	if err := emitEvent(ctx, tx, emitParams{
		NodeID:      id,
		ProjectCode: projectPrefixFromNodeID(id),
		OpType:      model.OpTransitionStatus,
		Author:      author,
		Payload:     payload,
	}); err != nil {
		return err
	}

	if cascade {
		if err := cascadeCancel(ctx, tx, id, reason, author, nowStr); err != nil {
			return fmt.Errorf("cascade cancel from %s: %w", id, err)
		}
	}

	if parentID != "" {
		if err := recalculateProgress(ctx, tx, parentID); err != nil {
			return fmt.Errorf("recalculate progress after cancel: %w", err)
		}
	}

	return nil
}

// readNodeForCancel reads the current status and parent ID for cancel validation.
func readNodeForCancel(ctx context.Context, tx *sql.Tx, id string) (model.Status, string, error) {
	var currentStatus, parentID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT status, parent_id FROM nodes
		 WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&currentStatus, &parentID)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return "", "", fmt.Errorf("read node %s for cancel: %w", id, err)
	}
	return model.Status(currentStatus.String), parentID.String, nil
}

// applyCancelUpdate sets the node to canceled and records the activity entry.
func applyCancelUpdate(ctx context.Context, tx *sql.Tx, id string, fromStatus model.Status, reason, author string, now time.Time, nowStr string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusCancelled), nowStr, nowStr, id,
	)
	if err != nil {
		return fmt.Errorf("cancel node %s: %w", id, err)
	}

	if err := appendActivityEntry(ctx, tx, id, model.ActivityEntry{
		ID:        fmt.Sprintf("act-%d", now.UnixNano()),
		Type:      model.ActivityTypeStatusChange,
		Author:    author,
		Text:      reason,
		CreatedAt: now,
		Metadata:  mustMarshal(map[string]string{"from_status": string(fromStatus), "to_status": string(model.StatusCancelled)}),
	}); err != nil {
		return fmt.Errorf("record cancel activity for %s: %w", id, err)
	}

	return nil
}

// cascadeCancel cancels all non-terminal descendants of a node.
// Uses LIKE pattern on dot-notation IDs for efficient subtree selection.
func cascadeCancel(ctx context.Context, tx *sql.Tx, parentID, _, _, nowStr string) error {
	// Cancel only non-terminal descendants.
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ?
		 WHERE id LIKE ? ESCAPE '\'
		   AND deleted_at IS NULL
		   AND status NOT IN (?, ?, ?)`,
		string(model.StatusCancelled), nowStr, nowStr,
		parentID+".%",
		string(model.StatusDone), string(model.StatusCancelled), string(model.StatusInvalidated),
	)
	if err != nil {
		return fmt.Errorf("cascade cancel descendants of %s: %w", parentID, err)
	}

	return nil
}
