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
		// A returned-seq mark above the tail would make a fresh event that reuses a
		// freed seq look already returned, so the inbox read would not charge it
		// (ADR-007 A12, MTIX-112.2.2.1.1). Lower only marks above the tail.
		`UPDATE agent_registrations SET returned_seq_mark = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)
         WHERE returned_seq_mark > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// The per-id meta mark of an agent with no registration row, lowered the same way
		// (the literal is inboxReturnedMetaPrefix, 19 bytes).
		`UPDATE meta SET value = CAST((SELECT COALESCE(MAX(rowid), 0) FROM sync_events) AS TEXT)
         WHERE substr(key, 1, 19) = 'inbox.returned_seq.' AND CAST(value AS INTEGER) > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim wake bookkeeping to the journal: %w", err)
		}
	}
	return nil
}
