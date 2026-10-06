// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// initialClaimAssignee selects the current parent assignment under the create write lock.
// The service requests parent inheritance; explicit assignment always takes precedence.
func initialClaimAssignee(ctx context.Context, tx *sql.Tx, node *model.Node, opts store.CreateNodeOptions) (string, error) {
	if !opts.ClaimParentAssignee || opts.Assignee != "" || node.ParentID == "" {
		return opts.Assignee, nil
	}
	var inherited string
	// A reopened/unassigned parent contributes no initial claim (FR-11.2a).
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(assignee, '') FROM nodes WHERE id = ? AND status = ?`, node.ParentID, model.StatusInProgress).Scan(&inherited)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read parent assignment: %w", err)
	}
	return inherited, nil
}

// allocateNodeID assigns the local sequence inside the insertion transaction (FR-2.7).
func allocateNodeID(ctx context.Context, tx *sql.Tx, node *model.Node, provisional bool) error {
	seq, err := nextSequenceQuery(ctx, tx, sequenceKey(node.Project, node.ParentID))
	if err != nil {
		return err
	}
	node.Seq = seq
	if provisional {
		node.ID, err = model.BuildProvisionalID(node.ParentID, node.UID)
		if err != nil {
			return fmt.Errorf("build provisional id: %w", err)
		}
	} else {
		node.ID = model.BuildID(node.Project, node.ParentID, seq)
	}
	return nil
}
