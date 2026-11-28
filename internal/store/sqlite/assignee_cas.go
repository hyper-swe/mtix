// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// checkExpectedAssignee is the compare-and-set read of an assignee change
// (MTIX-325.1). A nil expected skips the check. Otherwise the assignee read
// in this transaction must equal *expected ("" means unclaimed), else it
// returns ErrAlreadyClaimed and the caller writes nothing. Run it inside the
// write transaction so no claim or reassign can land between check and write.
func checkExpectedAssignee(ctx context.Context, tx *sql.Tx, id string, expected *string) error {
	if expected == nil {
		return nil
	}
	var current string
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(assignee, '') FROM nodes WHERE id = ? AND deleted_at IS NULL`, id,
	).Scan(&current)
	if err == sql.ErrNoRows {
		return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("read assignee of %s: %w", id, err)
	}
	if current != *expected {
		return fmt.Errorf("node %s: assignee changed concurrently (expected %q, now %q): %w",
			id, *expected, current, model.ErrAlreadyClaimed)
	}
	return nil
}
