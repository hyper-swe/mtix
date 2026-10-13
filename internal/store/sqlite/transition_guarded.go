// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// TransitionStatusFrom changes a node's status only if it is still in
// expectedFrom when the write transaction reads it (MTIX-151, FR-3.5).
// A caller that decided on a read made outside the transaction uses this so a
// concurrent change cannot be overwritten by a stale decision.
//
// Returns ErrConflict if the node's status is no longer expectedFrom,
// ErrNotFound if it does not exist, and ErrInvalidTransition as
// TransitionStatus does.
func (s *Store) TransitionStatusFrom(
	ctx context.Context, id string, expectedFrom, toStatus model.Status, reason, author string,
) error {
	// MTIX-95.31.19.2: withHistoryTx appends the task_history entries in this transaction.
	return s.withHistoryTx(ctx, historyScope{id: id, actor: author}, func(tx *sql.Tx) error {
		current, _, err := readNodeStatus(ctx, tx, id)
		if err != nil {
			return err
		}
		if current != expectedFrom {
			return fmt.Errorf("node %s is %s, not %s: %w", id, current, expectedFrom, model.ErrConflict)
		}
		return s.executeTransitionTx(ctx, tx, transitionRequest{id: id, toStatus: toStatus, reason: reason, author: author})
	})
}

// RestoreInvalidated returns an invalidated node to its previous_status
// (FR-3.5), reading status and previous_status inside the write transaction
// so the target is never derived from a stale snapshot (MTIX-151). When
// previous_status is empty, or not reachable from invalidated, the node
// returns to open, which is always valid from invalidated.
//
// Returns ErrInvalidTransition if the node is not invalidated and
// ErrNotFound if it does not exist or is soft-deleted.
func (s *Store) RestoreInvalidated(ctx context.Context, id, author string) (model.Status, error) {
	restoreTo := model.StatusOpen
	err := s.withHistoryTx(ctx, historyScope{id: id, actor: author}, func(tx *sql.Tx) error {
		// Read status and previous_status in the write transaction, so a
		// concurrent cancel or restore is seen before the restore is decided.
		var status string
		var prev sql.NullString
		scanErr := tx.QueryRowContext(ctx,
			`SELECT status, previous_status FROM nodes WHERE id = ? AND deleted_at IS NULL`, id,
		).Scan(&status, &prev)
		if scanErr == sql.ErrNoRows {
			return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
		}
		if scanErr != nil {
			return fmt.Errorf("read restore state for %s: %w", id, scanErr)
		}
		if model.Status(status) != model.StatusInvalidated {
			return fmt.Errorf(
				"cannot restore %s: current status is %s, not invalidated: %w",
				id, status, model.ErrInvalidTransition,
			)
		}
		if prev.Valid && prev.String != "" &&
			model.ValidateTransition(model.StatusInvalidated, model.Status(prev.String)) == nil {
			restoreTo = model.Status(prev.String)
		}
		return s.executeTransitionTx(ctx, tx, transitionRequest{
			id: id, toStatus: restoreTo, reason: "restored from invalidated", author: author,
		})
	})
	if err != nil {
		return "", err
	}
	return restoreTo, nil
}
