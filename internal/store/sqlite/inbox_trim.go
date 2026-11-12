// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// trimInboxToJournal removes every inbox bookkeeping row that points past the
// end of the journal, in the caller's transaction (MTIX-112.2).
//
// The ack ledger, the hook deliveries and the cumulative cursor are all keyed
// to sync_events.rowid, and sync_events has no AUTOINCREMENT: after a delete
// of the newest events the next insert takes the freed rowid again. A row left
// above the new tail would then cover an unrelated, later event, so new mail
// would arrive already acknowledged and never show in the inbox (the same
// class DiscardLocal clears for a full wipe, MTIX-66). Rows for freed rowids
// inside the journal are harmless, because SQLite only reuses rowids above the
// maximum. Chosen over a persisted high-water mark because it needs no schema
// change and covers every row the journal tail could reach.
// Each statement spells the journal tail, the highest sync_events rowid (0 for an
// empty journal), as a literal subquery: no SQL is built by concatenation.
func trimInboxToJournal(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		// Selective acks of events that no longer exist.
		`DELETE FROM agent_inbox_ack WHERE event_seq > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// Hook deliveries of events that no longer exist.
		`DELETE FROM inbox_deliveries WHERE event_seq > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// A watermark above the tail would hide every event up to it.
		`UPDATE agent_inbox_cursor SET cursor = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events) WHERE cursor > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// Delivery marks keyed to freed rowids: drop those wholly above the tail,
		// then clip a mark straddling it to the tail (MTIX-112.3).
		`DELETE FROM agent_inbox_cursor_marks WHERE from_cursor >= (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		`UPDATE OR IGNORE agent_inbox_cursor_marks SET cursor = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events) WHERE cursor > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		`DELETE FROM agent_inbox_cursor_marks WHERE cursor > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim inbox bookkeeping to the journal: %w", err)
		}
	}
	return nil
}
