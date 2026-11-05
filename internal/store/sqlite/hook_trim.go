// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// trimReplaceBookkeepingToJournal keeps inbox and hook state within the same
// surviving journal boundary, using the replace caller's transaction.
func trimReplaceBookkeepingToJournal(ctx context.Context, tx *sql.Tx) error {
	if err := trimInboxToJournal(ctx, tx); err != nil {
		return err
	}
	return trimHookDispatchToJournal(ctx, tx)
}

// trimHookDispatchToJournal removes dispatch state for freed rowids in the
// replace transaction (FR-7.8, MTIX-112.2.2). The next journal insert can reuse
// a rowid above the surviving tail; neither an old claim nor the scan floor
// may mark that unrelated event dispatched. Surviving rows remain unchanged.
func trimHookDispatchToJournal(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		// Remove terminal and in-flight claims belonging to removed events.
		`DELETE FROM hook_dispatch_ledger WHERE event_seq > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
		// Only rewind a floor beyond the tail, never consume surviving backlog.
		`UPDATE hook_dispatch_cursor SET cursor = (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)
    WHERE cursor > (SELECT COALESCE(MAX(rowid), 0) FROM sync_events)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim hook dispatch bookkeeping to the journal: %w", err)
		}
	}
	return nil
}
