// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// trimWakeToJournal reconciles local wake progress in the journal rewrite transaction
// (FR-20, MTIX-112.2.2.1), shared by replace imports and discard-local.
// Freed rowids cannot retain claims or marks covering unrelated future mail.
// Registration epochs, holds, lease ownership and consumed budgets stay intact.
func trimWakeToJournal(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		// Removed event numbers cannot retain pending or terminal wake claims.
		`DELETE FROM wake_ledger WHERE event_seq > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// Lower only shared scan marks above the surviving tail.
		`UPDATE wake_scan_cursor SET cursor = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)
         WHERE cursor > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// Preserve registration identity and holds while lowering above-tail progress.
		`UPDATE agent_registrations SET high_water = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)
         WHERE high_water > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// Preserve live lease ownership while lowering above-tail progress.
		`UPDATE wake_leases SET high_water = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)
         WHERE high_water > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim wake bookkeeping to the journal: %w", err)
		}
	}
	return nil
}
