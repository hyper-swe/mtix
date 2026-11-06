// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// retainedDroppedInboxIDs selects pending mail carried by the incoming journal
// for a task replace drops. The task is matched by UID before display number.
const retainedDroppedInboxIDs = `SELECT se.event_id FROM sync_events se
 WHERE se.sync_status='pending'
 AND se.event_id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?2))
 AND EXISTS (SELECT 1 FROM nodes n WHERE n.id=?1 AND ` + loggedNodeMatch + `)`

// retainDroppedInboxEvents keeps inbox rowids, coverage and local hook state,
// but retires pending sync work for a dropped task (FR-19.1, MTIX-112.2.1).
// The local-only marker prevents this mail implying hub contact or reaching a
// relay; it does not suppress existing hooks or replace their deliveries.
func retainDroppedInboxEvents(ctx context.Context, tx *sql.Tx, nodeID, retained string) error {
	// Remove held push copies before retiring the same pending identities.
	for _, statement := range []string{
		`DELETE FROM sync_quarantine WHERE source='push' AND event_id IN (` + retainedDroppedInboxIDs + `)`,
		`INSERT OR IGNORE INTO inbox_restored_events(event_id) ` + retainedDroppedInboxIDs,
		`UPDATE sync_events SET sync_status='applied' WHERE event_id IN (` + retainedDroppedInboxIDs + `)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, nodeID, retained); err != nil {
			return fmt.Errorf("retain dropped-task inbox mail locally: %w", err)
		}
	}
	return nil
}
