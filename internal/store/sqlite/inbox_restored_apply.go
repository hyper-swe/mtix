// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// upgradeRestoredInboxEvent adopts a real envelope without changing the arrival
// rowid, ack coverage or hook suppression (FR-19.1, MTIX-112.2.1).
func upgradeRestoredInboxEvent(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (bool, error) {
	query, err := restoredInboxQuery(ctx, tx, `SELECT author_id,payload FROM sync_events WHERE event_id=? AND (0)`, `SELECT author_id,payload FROM sync_events WHERE event_id=? AND (EXISTS (SELECT 1 FROM inbox_restored_events r WHERE r.event_id=sync_events.event_id))`)
	if err != nil {
		return false, err
	}
	stored := inboxJournalEvent{EventID: e.EventID}
	err = tx.QueryRowContext(ctx, query, e.EventID).Scan(&stored.Author, &stored.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read restored envelope: %w", err)
	}
	incoming := inboxJournalEvent{EventID: e.EventID, Author: e.AuthorID, Payload: string(e.Payload)}
	if e.OpType != model.OpComment || !sameInboxContent(stored, incoming) {
		return false, fmt.Errorf("pulled event conflicts with restored mail: %w", model.ErrConflict)
	}
	vc, err := json.Marshal(e.VectorClock)
	if err != nil {
		return false, fmt.Errorf("encode restored envelope clock: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE sync_events SET project_prefix=?,node_id=?,uid=?,payload=?,wall_clock_ts=?,lamport_clock=?,vector_clock=?,author_machine_hash=?,sync_status='applied' WHERE event_id=?`, e.ProjectPrefix, e.NodeID, nullableString(e.UID), string(e.Payload), e.WallClockTS, e.LamportClock, string(vc), e.AuthorMachineHash, e.EventID)
	if err != nil {
		return false, fmt.Errorf("upgrade restored envelope: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM inbox_restored_events WHERE event_id=?`, e.EventID); err != nil {
		return false, fmt.Errorf("retire restored provenance: %w", err)
	}
	return true, nil
}
