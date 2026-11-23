// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// relayForwardDDL tracks the provenance of pending relay rows (FR-21 §11.7).
// The journal remains the only copy of each envelope. Normal push settlement
// or deletion retires its marker in the same transaction.
const relayForwardDDL = `
CREATE TABLE IF NOT EXISTS relay_forward_pending (
    event_id TEXT PRIMARY KEY NOT NULL REFERENCES sync_events(event_id) ON DELETE CASCADE
);
CREATE TRIGGER IF NOT EXISTS retire_relay_forward
AFTER UPDATE OF sync_status ON sync_events
WHEN OLD.sync_status='pending' AND NEW.sync_status <> 'pending'
BEGIN
    DELETE FROM relay_forward_pending WHERE event_id=NEW.event_id;
END;`

// AcknowledgeRelayForward retires the relay forward of a hub copy of an event
// that a pull quarantined and relay ingest applied since; the quarantine retry
// calls it before it drops the hub envelope, so the event is never pushed back.
func AcknowledgeRelayForward(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	return acknowledgeRelayForward(ctx, tx, e)
}

// IsRelayForward reports whether eventID is a relay-origin event still
// pending for the hub.
func (s *Store) IsRelayForward(ctx context.Context, eventID string) (bool, error) {
	var n int
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM relay_forward_pending WHERE event_id=?`, eventID).Scan(&n); err != nil {
		return false, fmt.Errorf("read relay forward %s: %w", format.EventID(eventID), err)
	}
	return n > 0, nil
}

// acknowledgeRelayForward handles the other arrival order: relay first, then
// a hub pull before push acknowledgement. Keep an id/content mismatch pending
// for the hub's normal push integrity check (ADR-006 I6).
func acknowledgeRelayForward(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var payload string
	err := tx.QueryRowContext(ctx, `
		SELECT s.payload FROM relay_forward_pending f JOIN sync_events s ON s.event_id=f.event_id
		WHERE f.event_id=? AND s.node_id=? AND s.op_type=?`, e.EventID, e.NodeID, e.OpType).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pending relay event %s: %w", format.EventID(e.EventID), err)
	}
	// The hub's JSONB reorders keys and whitespace and keeps a number by value.
	same, err := samePayloadValue(payload, string(e.Payload))
	if err != nil {
		return err
	}
	if !same {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE sync_events SET sync_status='pushed' WHERE event_id=?`, e.EventID)
	if err != nil {
		return fmt.Errorf("acknowledge relay event %s: %w", format.EventID(e.EventID), err)
	}
	return nil
}

// IsRelayPublished reports whether the relay publish pass has already framed
// eventID: its journal row sits at or before the publish cursor, the same
// position ReadRelayJournalSince resumes from. A task whose creation is
// published is carried by the relay under its current number, so the hub must
// never move it (relay-carried tasks are never renumbered). A store with no
// cursor row has published nothing.
func (s *Store) IsRelayPublished(ctx context.Context, eventID string) (bool, error) {
	var n int
	if err := s.readDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_events e JOIN relay_push_cursor c ON c.id = 1
		WHERE e.event_id = ? AND e.rowid <= c.cursor`, eventID).Scan(&n); err != nil {
		return false, fmt.Errorf("read relay publish state of %s: %w", format.EventID(eventID), err)
	}
	return n > 0, nil
}
