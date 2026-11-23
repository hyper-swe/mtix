// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
)

// relayPublishedHWMKey is the meta key of the relay published high-water mark:
// the highest sync_events.rowid the publisher marked for framing (MarkRelayFraming) or
// advanced the publish cursor over. It only ever rises (DiscardLocal, which
// empties the journal and its rowid space, is the one reset). The publish
// cursor itself can be rewound (RepublishRelayFrom, ResetRelayPublisher), so
// it cannot say whether the relay carries an event.
const relayPublishedHWMKey = "meta.relay.published_hwm"

// raiseRelayPublishedHWM raises the mark to seq; a lower seq is absorbed.
func raiseRelayPublishedHWM(ctx context.Context, db relayExecer, seq int64) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, CAST(? AS TEXT))
		ON CONFLICT(key) DO UPDATE SET value = CAST(excluded.value AS TEXT)
		WHERE CAST(excluded.value AS INTEGER) > CAST(meta.value AS INTEGER)`,
		relayPublishedHWMKey, seq)
	if err != nil {
		return fmt.Errorf("raise relay published mark: %w", err)
	}
	return nil
}

// IsRelayPublished reports whether the relay carries eventID, or may carry it:
// its journal row is at or below the published high-water mark, or at or
// below the publish cursor (a store that published before the mark existed).
// The publisher raises the mark (MarkRelayFraming) before it
// appends, so the answer is true from the moment a row may be on the relay and
// no rewind lowers it. A task whose creation is published is carried by the
// relay under its current number, so the hub must never move it
// (relay-carried tasks are never renumbered).
func (s *Store) IsRelayPublished(ctx context.Context, eventID string) (bool, error) {
	var n int
	if err := s.readDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_events e
		WHERE e.event_id = ? AND e.rowid <= MAX(
		    COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key = ?), 0),
		    COALESCE((SELECT cursor FROM relay_push_cursor WHERE id = 1), 0))`,
		eventID, relayPublishedHWMKey).Scan(&n); err != nil {
		return false, fmt.Errorf("read relay publish state of %s: %w", format.EventID(eventID), err)
	}
	return n > 0, nil
}
