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
		if found == 0 {
			// Constant additive DDL preserves the original key and all legacy row values.
			if _, err := tx.ExecContext(ctx, "ALTER TABLE wake_ledger ADD COLUMN attempt_token TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("add wake attempt fence: %w", err)
			}
		}
		return addWakeResultColumns(ctx, tx)
	})
}

// wakeResultColumns are the additive adapter-outcome columns (MTIX-112.40.2). Old
// binaries ignore them: their INSERT and SELECT statements name their own columns.
func wakeResultColumns() []struct{ name, ddl string } {
	return []struct{ name, ddl string }{
		{"route", "ALTER TABLE wake_ledger ADD COLUMN route TEXT NOT NULL DEFAULT ''"},
		{"reason", "ALTER TABLE wake_ledger ADD COLUMN reason TEXT NOT NULL DEFAULT ''"},
		{"outcome_at", "ALTER TABLE wake_ledger ADD COLUMN outcome_at TEXT NOT NULL DEFAULT ''"},
	}
}

func addWakeResultColumns(ctx context.Context, tx *sql.Tx) error {
	for _, c := range wakeResultColumns() {
		var found int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM pragma_table_info(?) WHERE name=?", "wake_ledger", c.name).Scan(&found); err != nil {
			return fmt.Errorf("inspect wake result column %s: %w", c.name, err)
		}
		if found != 0 {
			continue
		}
		// Constant additive DDL; the statement text comes from the table above.
		if _, err := tx.ExecContext(ctx, c.ddl); err != nil {
			return fmt.Errorf("add wake result column %s: %w", c.name, err)
		}
	}
	return nil
}
