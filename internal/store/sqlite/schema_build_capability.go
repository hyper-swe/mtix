// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// initializeBuildCapability adds the additive capabilities column to the
// registration and heartbeat tables without changing schema_version
// (MTIX-95.31.19.6). Older binaries ignore it: their statements name their own
// columns. A row written by one of them keeps the empty default, which the
// history.export guard reads as an older reader.
func (s *Store) initializeBuildCapability(ctx context.Context) error {
	return s.withSchemaTx(ctx, func(tx *sql.Tx) error {
		for _, c := range []struct{ table, ddl string }{
			{"agent_registrations", "ALTER TABLE agent_registrations ADD COLUMN capabilities TEXT NOT NULL DEFAULT ''"},
			{"dispatcher_heartbeat", "ALTER TABLE dispatcher_heartbeat ADD COLUMN capabilities TEXT NOT NULL DEFAULT ''"},
		} {
			var found int
			if err := tx.QueryRowContext(ctx,
				"SELECT count(*) FROM pragma_table_info(?) WHERE name=?", c.table, "capabilities").Scan(&found); err != nil {
				return fmt.Errorf("inspect %s capabilities: %w", c.table, err)
			}
			if found != 0 {
				continue
			}
			// Constant additive DDL; the statement text comes from the table above.
			if _, err := tx.ExecContext(ctx, c.ddl); err != nil {
				return fmt.Errorf("add %s capabilities: %w", c.table, err)
			}
		}
		return nil
	})
}
