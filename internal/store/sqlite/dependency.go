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

// AddDependency creates a dependency between two nodes per FR-4.2.
// For 'blocks' type, auto-sets the target node to 'blocked' if it is
// currently in 'open' or 'in_progress' status per FR-3.8.
// Saves previous_status for auto-restore when blockers clear.
//
// Returns ErrCycleDetected for circular block dependencies (FR-4.3).
// Returns ErrAlreadyExists if the dependency already exists.
func (s *Store) AddDependency(ctx context.Context, dep *model.Dependency) error {
	if err := dep.Validate(); err != nil {
		return err
	}

	return s.WithTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)

		// For blocks dependencies, check for cycles (FR-4.3).
		if dep.DepType == model.DepTypeBlocks {
			if err := detectCycle(ctx, tx, dep.FromID, dep.ToID); err != nil {
				return err
			}
		}

		// Insert the dependency.
		_, err := tx.ExecContext(ctx,
			`INSERT INTO dependencies (from_id, to_id, dep_type, created_at, created_by, metadata)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			dep.FromID, dep.ToID, string(dep.DepType), now,
			nullableString(dep.CreatedBy),
			nullableString(string(dep.Metadata)),
		)
		if err != nil {
			if isUniqueConstraintError(err) {
				return fmt.Errorf("dependency %s→%s (%s): %w",
					dep.FromID, dep.ToID, dep.DepType, model.ErrAlreadyExists)
			}
			return fmt.Errorf("insert dependency: %w", err)
		}

		// FR-3.8: Auto-block the target node if it's open or in_progress.
		if dep.DepType == model.DepTypeBlocks {
			if err := autoBlockNode(ctx, tx, dep.ToID); err != nil {
				return fmt.Errorf("auto-block %s: %w", dep.ToID, err)
			}
		}

		return nil
	})
}

// RemoveDependency removes a dependency between two nodes.
// For 'blocks' type, checks if the target node should be auto-unblocked
// (no remaining unresolved blockers) per FR-3.8.
func (s *Store) RemoveDependency(ctx context.Context, fromID, toID string, depType model.DepType) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`DELETE FROM dependencies
			 WHERE from_id = ? AND to_id = ? AND dep_type = ?`,
			fromID, toID, string(depType),
		)
		if err != nil {
			return fmt.Errorf("remove dependency: %w", err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check rows affected: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("dependency %s→%s (%s): %w",
				fromID, toID, depType, model.ErrNotFound)
		}

		// FR-3.8: Auto-unblock if this was the last blocker.
		if depType == model.DepTypeBlocks {
			if err := autoUnblockNode(ctx, tx, toID); err != nil {
				return fmt.Errorf("auto-unblock %s: %w", toID, err)
			}
		}

		return nil
	})
}

// GetBlockers returns all unresolved blocking dependencies for a node.
// A blocker is unresolved if the source node is not in a terminal status.
func (s *Store) GetBlockers(ctx context.Context, nodeID string) ([]*model.Dependency, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT d.from_id, d.to_id, d.dep_type, d.created_at, d.created_by, d.metadata
		 FROM dependencies d
		 JOIN nodes n ON d.from_id = n.id
		 WHERE d.to_id = ? AND d.dep_type = ?
		   AND n.status NOT IN (?, ?, ?)
		   AND n.deleted_at IS NULL`,
		nodeID, string(model.DepTypeBlocks),
		string(model.StatusDone), string(model.StatusCancelled), string(model.StatusInvalidated),
	)
	if err != nil {
		return nil, fmt.Errorf("query blockers for %s: %w", nodeID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var deps []*model.Dependency
	for rows.Next() {
		var dep model.Dependency
		var createdBy, metadata sql.NullString
		var createdAt string

		if err := rows.Scan(&dep.FromID, &dep.ToID, &dep.DepType,
			&createdAt, &createdBy, &metadata); err != nil {
			return nil, fmt.Errorf("scan blocker: %w", err)
		}

		dep.CreatedBy = createdBy.String
		dep.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		deps = append(deps, &dep)
	}

	return deps, rows.Err()
}

// autoBlockNode auto-sets a node to blocked if it's open or in_progress per FR-3.8.
// Saves previous_status for auto-restore.
// Does NOT auto-block deferred, done, canceled, or invalidated nodes.
func autoBlockNode(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var currentStatus string
	err := tx.QueryRowContext(ctx,
		`SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		nodeID,
	).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("read status for auto-block: %w", err)
	}

	status := model.Status(currentStatus)

	// FR-3.8: Only auto-block open or in_progress nodes.
	if status != model.StatusOpen && status != model.StatusInProgress {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, previous_status = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusBlocked), currentStatus, now, nodeID,
	)
	if err != nil {
		return fmt.Errorf("auto-block node %s: %w", nodeID, err)
	}

	return nil
}

// autoUnblockNode checks if a blocked node has no more unresolved blockers
// and auto-restores it to previous_status per FR-3.8.
func autoUnblockNode(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var currentStatus, previousStatus sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT status, previous_status FROM nodes
		 WHERE id = ? AND deleted_at IS NULL`,
		nodeID,
	).Scan(&currentStatus, &previousStatus)
	if err != nil {
		return fmt.Errorf("read status for auto-unblock: %w", err)
	}

	// Only auto-unblock if currently blocked.
	if model.Status(currentStatus.String) != model.StatusBlocked {
		return nil
	}

	// FR-3.8a: Invalidated takes precedence — don't auto-unblock.
	// (If the node somehow got to blocked after invalidation, respect it.)

	// Check if there are remaining unresolved blockers.
	var blockerCount int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies d
		 JOIN nodes n ON d.from_id = n.id
		 WHERE d.to_id = ? AND d.dep_type = ?
		   AND n.status NOT IN (?, ?, ?)
		   AND n.deleted_at IS NULL`,
		nodeID, string(model.DepTypeBlocks),
		string(model.StatusDone), string(model.StatusCancelled), string(model.StatusInvalidated),
	).Scan(&blockerCount)
	if err != nil {
		return fmt.Errorf("count remaining blockers for %s: %w", nodeID, err)
	}

	if blockerCount > 0 {
		return nil // Still has unresolved blockers.
	}

	// Restore to previous_status.
	restoreTo := model.StatusOpen
	if previousStatus.Valid && previousStatus.String != "" {
		restoreTo = model.Status(previousStatus.String)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, previous_status = NULL, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(restoreTo), now, nodeID,
	)
	if err != nil {
		return fmt.Errorf("auto-unblock node %s: %w", nodeID, err)
	}

	return nil
}

// detectCycle checks if adding a blocks dependency from→to would create a cycle.
// Uses iterative BFS: starts from toID and follows existing outgoing blocks edges.
// If toID can reach fromID through existing blocks, adding from→to creates a cycle.
func detectCycle(ctx context.Context, tx *sql.Tx, fromID, toID string) error {
	visited := make(map[string]bool)
	queue := []string{toID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current == fromID {
			return fmt.Errorf(
				"adding blocks dependency %s→%s would create a cycle: %w",
				fromID, toID, model.ErrCycleDetected,
			)
		}

		if visited[current] {
			continue
		}
		visited[current] = true

		// Find all nodes that this node blocks.
		neighbors, err := cycleNeighbors(ctx, tx, current)
		if err != nil {
			return err
		}
		queue = append(queue, neighbors...)
	}

	return nil
}

// cycleNeighbors returns all nodes that the given node blocks via dependencies.
// Used by detectCycle for BFS traversal.
func cycleNeighbors(ctx context.Context, tx *sql.Tx, nodeID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT to_id FROM dependencies
		 WHERE from_id = ? AND dep_type = ?`,
		nodeID, string(model.DepTypeBlocks),
	)
	if err != nil {
		return nil, fmt.Errorf("query dependencies for cycle detection: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var neighbors []string
	for rows.Next() {
		var nextID string
		if err := rows.Scan(&nextID); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		neighbors = append(neighbors, nextID)
	}
	return neighbors, rows.Err()
}
