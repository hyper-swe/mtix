// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// realignMovedColumns sets, inside RenameTo's or ImportAs's transaction tx,
// the project, seq and depth of every task the command moved to the values
// its new id names (MTIX-107.58): the id's prefix, its last number and the
// number of dots after the prefix. mapping maps each old id to its new id,
// and the rename loop has already written the new ids. It runs before the
// counters are raised, so a child counter is keyed by the new project, as
// a create under that parent is.
func realignMovedColumns(ctx context.Context, tx *sql.Tx, mapping map[string]string) error {
	for _, newID := range mapping {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET project = ?, seq = ?, depth = ? WHERE id = ?`,
			model.ParseIDProject(newID), deriveSeq(newID), model.ParseIDDepth(newID), newID,
		); err != nil {
			return fmt.Errorf("set project, seq and depth of %s: %w", newID, err)
		}
	}
	return nil
}
