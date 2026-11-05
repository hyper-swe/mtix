// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// trimWakeToJournal reconciles local wake progress in the replace transaction.
// Freed rowids cannot retain claims or marks covering unrelated future mail.
// Registration epochs, holds, lease ownership and consumed budgets stay intact.
func trimWakeToJournal(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DELETE FROM wake_ledger WHERE event_seq > ` + journalTailSQL,
		`UPDATE wake_scan_cursor SET cursor = ` + journalTailSQL + ` WHERE cursor > ` + journalTailSQL,
		`UPDATE agent_registrations SET high_water = ` + journalTailSQL + ` WHERE high_water > ` + journalTailSQL,
		`UPDATE wake_leases SET high_water = ` + journalTailSQL + ` WHERE high_water > ` + journalTailSQL,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim wake bookkeeping to the journal: %w", err)
		}
	}
	return nil
}
