// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// recalculateProgress recomputes the progress of a node based on its
// direct children's progress and weights, then recurses up to the root.
// This MUST be called within a transaction per FR-5.7.
//
// Progress formula per FR-5.1–5.9:
//   - Leaf nodes (no children): retain current progress value
//   - Parent nodes: sum(child.progress * child.weight) / sum(child.weight)
//   - Canceled children are EXCLUDED from the denominator (FR-5.4)
//   - Invalidated children are EXCLUDED from the denominator (FR-5.6)
//   - Deferred children ARE included (they represent pending work) (FR-5.5)
//   - If ALL children are excluded, progress is 0.0 (FR-5.6b)
func recalculateProgress(ctx context.Context, tx *sql.Tx, nodeID string) error {
	if nodeID == "" {
		return nil
	}

	// Compute weighted average progress from direct non-deleted,
	// non-canceled, non-invalidated children per FR-5.3/5.4/5.6.
	var totalWeight, weightedSum sql.NullFloat64
	err := tx.QueryRowContext(ctx,
		`SELECT SUM(weight), SUM(progress * weight)
		 FROM nodes
		 WHERE parent_id = ? AND deleted_at IS NULL
		   AND status NOT IN (?, ?)`,
		nodeID,
		string(model.StatusCancelled),
		string(model.StatusInvalidated),
	).Scan(&totalWeight, &weightedSum)
	if err != nil {
		return fmt.Errorf("query children progress for %s: %w", nodeID, err)
	}

	// FR-5.6b: If all children are excluded (or no children), set progress to 0.0.
	progress := 0.0
	if totalWeight.Valid && totalWeight.Float64 > 0 {
		progress = weightedSum.Float64 / totalWeight.Float64
	}

	// Update the node's progress.
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET progress = ? WHERE id = ? AND deleted_at IS NULL`,
		progress, nodeID,
	)
	if err != nil {
		return fmt.Errorf("update progress for %s: %w", nodeID, err)
	}

	// Recurse to parent per FR-5.7.
	var parentID sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT parent_id FROM nodes WHERE id = ?`,
		nodeID,
	).Scan(&parentID)
	if err != nil {
		return fmt.Errorf("get parent of %s: %w", nodeID, err)
	}

	if parentID.Valid && parentID.String != "" {
		return recalculateProgress(ctx, tx, parentID.String)
	}

	return nil
}
