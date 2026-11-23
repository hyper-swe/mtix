// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

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
func recalculateProgress(ctx context.Context, tx *sql.Tx, weighted bool, nodeID string) error {
	if nodeID == "" {
		return nil
	}

	if err := recomputeNodeProgress(ctx, tx, weighted, nodeID); err != nil {
		return err
	}

	// Recurse to parent per FR-5.7.
	var parentID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT parent_id FROM nodes WHERE id = ?`,
		nodeID,
	).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		// The node itself does not exist yet. This never happens on the
		// local paths (the node is always live), but the sync-apply path
		// can recompute a parent whose create_node has not yet applied
		// (causal order, HAZARD (c)). Treat as "no rollup target" rather
		// than an error so a single out-of-order event cannot wedge the
		// whole apply batch.
		return nil
	}
	if err != nil {
		return fmt.Errorf("get parent of %s: %w", nodeID, err)
	}

	if parentID.Valid && parentID.String != "" {
		return recalculateProgress(ctx, tx, weighted, parentID.String)
	}

	return nil
}

// recomputeNodeProgress recomputes the progress of nodeID alone and does not
// walk up. recalculateProgress calls it once per level of its walk to the
// root; a cascade cancel calls it once per ancestor inside the cancelled
// subtree, deepest first (MTIX-95.21). A soft-deleted node is left unchanged.
func recomputeNodeProgress(ctx context.Context, tx *sql.Tx, weighted bool, nodeID string) error {
	progress, err := computeNodeProgress(ctx, tx, weighted, nodeID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET progress = ? WHERE id = ? AND deleted_at IS NULL`,
		progress, nodeID,
	)
	if err != nil {
		return fmt.Errorf("update progress for %s: %w", nodeID, err)
	}
	return nil
}

// progressQuerier is the read surface shared by the write rollup (a
// transaction) and the verify check (the read pool), so both compute progress
// with one definition.
type progressQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// progressChild is one included (not cancelled, not invalidated) direct child.
type progressChild struct {
	done     bool    // status done and settled: progress 1.0, or no included child of its own (FR-5.6b)
	progress float64 // effective progress: 1.0 for a settled done child
	weight   float64
}

// computeNodeProgress derives a node's progress from its live children
// (MTIX-294). A node with no live child is a leaf: 1.0 when done, else 0.0
// (FR-5.2), so reopening or claiming a leaf drops its credit. Otherwise the
// default is count(done) / count(included) (FR-5.3), where a child counts as
// done only when its own progress is also 1.0 (or all its own children are
// excluded, FR-5.6b), so a done node with active
// descendants earns no credit for its ancestors; an open (unclaimed) node whose
// own progress is 1.0 (every descendant done) also counts, a claimed one does not; progress.weighted switches to
// the weighted mean (FR-5.8). A weighted result never reads 1.0 while an
// included child is below 1.0 (zero weight, float rounding). All children
// excluded gives 0.0 (FR-5.6b).
func computeNodeProgress(ctx context.Context, tx progressQuerier, weighted bool, nodeID string) (float64, error) {
	var live int
	var status sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM nodes WHERE parent_id = ? AND deleted_at IS NULL),
		        (SELECT status FROM nodes WHERE id = ?)`,
		nodeID, nodeID,
	).Scan(&live, &status)
	if err != nil {
		return 0, fmt.Errorf("count children of %s: %w", nodeID, err)
	}
	if live == 0 {
		if status.String == string(model.StatusDone) {
			return 1.0, nil
		}
		return 0.0, nil
	}
	children, err := includedChildren(ctx, tx, nodeID)
	if err != nil {
		return 0, err
	}
	if weighted {
		return weightedProgress(children), nil
	}
	return countProgress(children), nil
}

// includedChildren reads the direct children that count toward progress.
func includedChildren(ctx context.Context, tx progressQuerier, nodeID string) ([]progressChild, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT (status = ? AND (progress >= 1.0 OR NOT EXISTS (
		            SELECT 1 FROM nodes g WHERE g.parent_id = c.id AND g.deleted_at IS NULL
		              AND g.status NOT IN (?, ?))))
		        OR (status = ? AND progress >= 1.0 AND EXISTS (
		            SELECT 1 FROM nodes g WHERE g.parent_id = c.id AND g.deleted_at IS NULL
		              AND g.status NOT IN (?, ?))),
		        progress, weight FROM nodes c
		 WHERE parent_id = ? AND deleted_at IS NULL AND status NOT IN (?, ?)`,
		string(model.StatusDone), string(model.StatusCancelled), string(model.StatusInvalidated),
		string(model.StatusOpen), string(model.StatusCancelled), string(model.StatusInvalidated),
		nodeID, string(model.StatusCancelled), string(model.StatusInvalidated),
	)
	if err != nil {
		return nil, fmt.Errorf("query children progress for %s: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []progressChild
	for rows.Next() {
		var c progressChild
		if scanErr := rows.Scan(&c.done, &c.progress, &c.weight); scanErr != nil {
			return nil, fmt.Errorf("scan children progress for %s: %w", nodeID, scanErr)
		}
		if c.done {
			c.progress = 1.0
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate children progress for %s: %w", nodeID, err)
	}
	return out, nil
}

func countProgress(children []progressChild) float64 {
	if len(children) == 0 {
		return 0.0
	}
	done := 0
	for _, c := range children {
		if c.done {
			done++
		}
	}
	return float64(done) / float64(len(children))
}

func weightedProgress(children []progressChild) float64 {
	var total, sum float64
	unfinished := false
	for _, c := range children {
		total += c.weight
		sum += c.progress * c.weight
		if !c.done {
			unfinished = true
		}
	}
	if total <= 0 {
		return 0.0
	}
	p := sum / total
	if unfinished && p >= 1.0 {
		return math.Nextafter(1.0, 0.0)
	}
	return p
}
