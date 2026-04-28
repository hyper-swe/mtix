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

// DeleteNode soft-deletes a node per FR-3.3.
// Sets deleted_at and deleted_by on the target node.
// When cascade is true (default), all descendants are also soft-deleted.
// Recalculates parent progress excluding the deleted subtree per FR-5.7.
//
// Returns ErrNotFound if the node does not exist or is already deleted.
func (s *Store) DeleteNode(ctx context.Context, id string, cascade bool, deletedBy string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)

		// Verify node exists and is not already deleted.
		var parentID sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT parent_id FROM nodes WHERE id = ? AND deleted_at IS NULL`,
			id,
		).Scan(&parentID)
		if err == sql.ErrNoRows {
			return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("check node %s: %w", id, err)
		}

		// Soft-delete the target node.
		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ?
			 WHERE id = ? AND deleted_at IS NULL`,
			now, deletedBy, now, id,
		)
		if err != nil {
			return fmt.Errorf("soft-delete node %s: %w", id, err)
		}

		// Cascade to descendants if requested.
		if cascade {
			if err := cascadeDelete(ctx, tx, id, deletedBy, now); err != nil {
				return fmt.Errorf("cascade delete from %s: %w", id, err)
			}
		}

		payload, _ := model.EncodePayload(&model.DeletePayload{})
		if err := emitEvent(ctx, tx, emitParams{
			NodeID:      id,
			ProjectCode: projectPrefixFromNodeID(id),
			OpType:      model.OpDelete,
			Author:      deletedBy,
			Payload:     payload,
		}); err != nil {
			return err
		}

		// FR-5.7: Recalculate parent progress.
		if parentID.Valid && parentID.String != "" {
			if err := recalculateProgress(ctx, tx, parentID.String); err != nil {
				return fmt.Errorf("recalculate progress after delete: %w", err)
			}
		}

		return nil
	})
}

// cascadeDelete soft-deletes all descendants of a node using a recursive approach.
// Uses iterative descent through the hierarchy via LIKE pattern on dot-notation IDs.
func cascadeDelete(ctx context.Context, tx *sql.Tx, parentID, deletedBy, now string) error {
	// Soft-delete all descendants using parameterized LIKE with ESCAPE.
	// Dot-notation means all descendants have the parent ID as a prefix.
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ?
		 WHERE id LIKE ? ESCAPE '\' AND deleted_at IS NULL`,
		now, deletedBy, now, parentID+".%",
	)
	if err != nil {
		return fmt.Errorf("cascade delete descendants of %s: %w", parentID, err)
	}

	return nil
}

// UndeleteNode restores a soft-deleted node and its descendants per FR-3.3.
// Clears deleted_at and deleted_by. Recalculates parent progress.
//
// Sync note (MTIX-15.2.3): UndeleteNode does NOT emit a sync_events row.
// Tombstones are monotonic per SYNC-DESIGN section 8.3 — a delete event
// once applied stays applied. Local restore is a single-CLI convenience
// (the row is recovered from the same DB it never left); cross-CLI
// restore must be done by a fresh create_node event under a new ID.
//
// Returns ErrNotFound if the node does not exist or is not deleted.
func (s *Store) UndeleteNode(ctx context.Context, id string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)

		// Verify node exists and IS deleted.
		var parentID sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT parent_id FROM nodes WHERE id = ? AND deleted_at IS NOT NULL`,
			id,
		).Scan(&parentID)
		if err == sql.ErrNoRows {
			return fmt.Errorf("deleted node %s: %w", id, model.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("check deleted node %s: %w", id, err)
		}

		// Restore the target node.
		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET deleted_at = NULL, deleted_by = NULL, updated_at = ?
			 WHERE id = ?`,
			now, id,
		)
		if err != nil {
			return fmt.Errorf("undelete node %s: %w", id, err)
		}

		// Restore all descendants.
		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET deleted_at = NULL, deleted_by = NULL, updated_at = ?
			 WHERE id LIKE ? ESCAPE '\'`,
			now, id+".%",
		)
		if err != nil {
			return fmt.Errorf("undelete descendants of %s: %w", id, err)
		}

		// FR-5.7: Recalculate parent progress.
		if parentID.Valid && parentID.String != "" {
			if err := recalculateProgress(ctx, tx, parentID.String); err != nil {
				return fmt.Errorf("recalculate progress after undelete: %w", err)
			}
		}

		return nil
	})
}
