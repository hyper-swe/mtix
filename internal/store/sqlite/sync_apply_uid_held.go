// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// refuseUIDHeldByOtherNode fails a create whose uid a local task at another
// id already holds (MTIX-95.31.8): the unique uid index would reject the
// insert, and ON CONFLICT (id) would not catch it. The error names the uid
// and the holder; pull quarantines the event (sync_quarantine), so it shows in
// `mtix sync quarantine list` and the doctor, and every pull retries it, so
// it applies once the duplicate is repaired. The same task at the same id
// (a replay) is not refused.
func refuseUIDHeldByOtherNode(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, uid string) error {
	var holder string
	// The task, soft-deleted ones included, that holds uid at another id.
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE uid = ? AND uid <> '' AND id <> ? LIMIT 1`,
		uid, e.NodeID).Scan(&holder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply create_node %s: look up uid holder: %w", e.EventID, err)
	}
	return fmt.Errorf("apply create_node %s: uid %s is held by local task %s: %w",
		e.EventID, uid, holder, model.ErrConflict)
}
