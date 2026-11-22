// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// reopensBlocker reports whether a status change takes a blocker out of a
// resolved status (done, cancelled or invalidated) into an unresolved one, so
// the nodes it blocks must be re-evaluated (MTIX-112.17.16.40, FR-3.8).
func reopensBlocker(from, to model.Status) bool {
	return isResolvingStatus(from) && !isResolvingStatus(to)
}

// blocksDependentIDs lists the nodes that blockerID blocks: the to_id of every
// blocks edge whose from_id is blockerID.
func blocksDependentIDs(ctx context.Context, tx *sql.Tx, blockerID string) ([]string, error) {
	// Forward blocks edges of one node; the dependents of a blocker.
	rows, err := tx.QueryContext(ctx,
		`SELECT to_id FROM dependencies
		 WHERE from_id = ? AND dep_type = ?`,
		blockerID, string(model.DepTypeBlocks))
	if err != nil {
		return nil, fmt.Errorf("read forward blocks deps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var toID string
		if scanErr := rows.Scan(&toID); scanErr != nil {
			return nil, fmt.Errorf("scan forward dep: %w", scanErr)
		}
		ids = append(ids, toID)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iter forward deps: %w", iterErr)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("close forward deps: %w", closeErr)
	}
	return ids, nil
}

// autoBlockDependents walks every node that blockerID blocks and calls
// autoBlockNode on it. autoBlockNode applies the shared unresolved-blocker
// rule, so only open and in_progress dependents with an unresolved blocker
// change (FR-3.8). It runs in the caller's transaction.
func autoBlockDependents(ctx context.Context, tx *sql.Tx, h historyToucher, blockerID string, clocks ...func() time.Time) error {
	ids, err := blocksDependentIDs(ctx, tx, blockerID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := autoBlockNode(ctx, tx, h, id, clocks...); err != nil {
			return fmt.Errorf("block %s: %w", id, err)
		}
	}
	return nil
}

// statusBeforeApply reads the status the event's node holds before the event
// is applied; "" when the node has no live row (the apply is then a no-op).
func statusBeforeApply(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (model.Status, error) {
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return "", err
	}
	var st sql.NullString
	// The node's current status, read before the winning write replaces it.
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`, id).Scan(&st)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("apply transition_status %s: read prior status: %w", format.EventID(e.EventID), err)
	}
	return model.Status(st.String), nil
}
