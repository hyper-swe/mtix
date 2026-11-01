// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Hook suppression records workflow decisions at ingest, not at dispatch.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Additive local-only state, created on every open without a schema version
// change. Cascading deletion follows journal discard; rowid reuse is harmless.
const hookSuppressionSQL = `
CREATE TABLE IF NOT EXISTS hook_suppressed_events (
    event_id TEXT PRIMARY KEY REFERENCES sync_events(event_id) ON DELETE CASCADE
);
`

// suppressWorkflowHook skips FR-19 delivery for a workflow event that changed
// no node column. The marker commits or rolls back with the replay transaction.
func suppressWorkflowHook(ctx context.Context, tx *sql.Tx, eventID string) error {
	// Record that this event lost at ingest, in the replay transaction, so the
	// journal read skips it; the key is unique, and replays return earlier.
	_, err := tx.ExecContext(ctx,
		`INSERT INTO hook_suppressed_events (event_id) VALUES (?)`, eventID)
	if err != nil {
		return fmt.Errorf("suppress losing workflow hook: %w", err)
	}
	return nil
}
