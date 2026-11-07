// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// seedWakeProgressAtTailTx shares the hook bootstrap writer snapshot (FR-20,
// MTIX-112.2.2.1). Only existing below-tail progress rises; unread history stays
// in the inbox, and holds, epochs, dirty leases, ownership and budgets stay intact.
func seedWakeProgressAtTailTx(ctx context.Context, tx *sql.Tx, tail int64) error {
	for _, stmt := range []string{
		// Existing shared scan progress must not enumerate bootstrap history.
		`UPDATE wake_scan_cursor SET cursor=? WHERE cursor<?`,
		// Automatic scanner and pi candidates start after the imported history.
		`UPDATE agent_registrations SET high_water=? WHERE high_water<?`,
		// Preserve the current holder and dirty flag while seeding lease progress.
		`UPDATE wake_leases SET high_water=? WHERE high_water<?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, tail, tail); err != nil {
			return fmt.Errorf("seed wake progress at bootstrap tail: %w", err)
		}
	}
	return nil
}
