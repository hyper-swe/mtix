// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// advanceRenamedCounters raises, inside RenameTo's transaction tx, the
// counters of the namespaces the rename created (MTIX-95.38): the root
// namespace of newPrefix and the child namespace of every node now in it,
// each to the highest number its ids hold. It reuses the counter helpers
// of a renumber (sequence_advance.go). The project, seq and depth columns
// stay as they were (MTIX-107.58).
func advanceRenamedCounters(ctx context.Context, tx *sql.Tx, newPrefix string) error {
	if err := advanceRootCounter(ctx, tx, newPrefix); err != nil {
		return fmt.Errorf("rename-to %s: %w", newPrefix, err)
	}
	if err := advanceChildCounters(ctx, tx, "", newPrefix+"-"); err != nil {
		return fmt.Errorf("rename-to %s: %w", newPrefix, err)
	}
	return nil
}

// advanceImportedCounters raises, inside ImportAs's transaction tx, the
// counters of the namespaces the import filled (MTIX-95.38): the child
// namespace of parentID, which now holds the former roots, and of every
// node under it, each to the highest number its ids hold.
func advanceImportedCounters(ctx context.Context, tx *sql.Tx, parentID string) error {
	if err := advanceChildCounters(ctx, tx, parentID, parentID+"."); err != nil {
		return fmt.Errorf("import-as %s: %w", parentID, err)
	}
	return nil
}
