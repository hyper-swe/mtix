// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// JournalEvent is a raw journaled mutation the hook dispatcher consumes
// (MTIX-47.3). Seq is the sync_events rowid — the monotonic local sequence the
// hook dispatch cursor tracks. Synced is true when the event arrived via hub
// replication (it is recorded in applied_events) rather than a local mutation.
type JournalEvent struct {
	Seq       int64
	EventID   string
	OpType    string
	NodeID    string
	Author    string
	Payload   []byte
	Synced    bool
	CreatedAt string
}

// ReadJournalSince returns journaled events with rowid greater than cursor,
// oldest first, up to limit. Synced is derived from applied_events (an event
// applied from the hub is recorded there; a locally-emitted one is not), so the
// dispatcher can honor a hook's include-synced opt-in (FR-19 §3).
func (s *Store) ReadJournalSince(ctx context.Context, cursor int64, limit int) ([]JournalEvent, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT e.rowid, e.event_id, e.op_type, e.node_id, e.author_id, e.payload,
		       e.created_at, (a.event_id IS NOT NULL) AS synced
		  FROM sync_events e
		  LEFT JOIN applied_events a ON a.event_id = e.event_id
		 WHERE e.rowid > ?
		 ORDER BY e.rowid ASC
		 LIMIT ?`, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("read journal since %d: %w", cursor, err)
	}
	defer func() { _ = rows.Close() }()

	var out []JournalEvent
	for rows.Next() {
		var (
			ev      JournalEvent
			payload string
		)
		if scanErr := rows.Scan(&ev.Seq, &ev.EventID, &ev.OpType, &ev.NodeID,
			&ev.Author, &payload, &ev.CreatedAt, &ev.Synced); scanErr != nil {
			return nil, fmt.Errorf("scan journal event: %w", scanErr)
		}
		ev.Payload = []byte(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate journal: %w", err)
	}
	return out, nil
}

// HookCursor returns the highest sync_events.rowid the dispatcher has processed
// (0 when nothing has been dispatched yet).
func (s *Store) HookCursor(ctx context.Context) (int64, error) {
	var c int64
	err := s.readDB.QueryRowContext(ctx,
		`SELECT cursor FROM hook_dispatch_cursor WHERE id = 1`).Scan(&c)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read hook cursor: %w", err)
	}
	return c, nil
}

// AdvanceHookCursor moves the dispatch watermark forward to seq (monotonic; a
// lower seq never rewinds it).
func (s *Store) AdvanceHookCursor(ctx context.Context, seq int64) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hook_dispatch_cursor (id, cursor) VALUES (1, ?)
			ON CONFLICT(id) DO UPDATE SET cursor = MAX(cursor, excluded.cursor)`, seq)
		return err
	})
}

// RecordInboxDelivery records that the event at eventSeq is delivered to
// agentID's inbox by hook hookName (FR-19.4). Idempotent per (agent, event), so
// re-dispatch of the same event never double-delivers.
func (s *Store) RecordInboxDelivery(ctx context.Context, agentID string, eventSeq int64, hookName string) error {
	if agentID == "" {
		return fmt.Errorf("inbox delivery: agent id required: %w", model.ErrInvalidInput)
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO inbox_deliveries (agent_id, event_seq, hook_name, delivered_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(agent_id, event_seq) DO NOTHING`,
			agentID, eventSeq, hookName, time.Now().UTC().Format(time.RFC3339))
		return err
	})
}
