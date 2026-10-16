// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// initializeWakeLedger adds the attempt fence without changing schema_version (ADR-007 §6.1).
// Detection and alteration share the schema writer lock, including across processes.
func (s *Store) initializeWakeLedger(ctx context.Context) error {
	return s.withSchemaTx(ctx, func(tx *sql.Tx) error {
		var found int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM pragma_table_info(?) WHERE name=?", "wake_ledger", "attempt_token").Scan(&found); err != nil {
			return fmt.Errorf("inspect wake attempt fence: %w", err)
		}
		if found != 0 {
			return nil
		}
		// Constant additive DDL preserves the original key and all legacy row values.
		if _, err := tx.ExecContext(ctx, "ALTER TABLE wake_ledger ADD COLUMN attempt_token TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("add wake attempt fence: %w", err)
		}
		return nil
	})
}
