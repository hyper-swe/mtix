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
	// ViaHook names the hook whose exec command journaled this event (empty for
	// an ordinary mutation). A via-hook event never re-triggers the same hook
	// (loop prevention, FR-19.6).
	ViaHook string
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
		       e.created_at, (a.event_id IS NOT NULL) AS synced,
		       COALESCE(o.via_hook, '') AS via_hook
		  FROM sync_events e
		  LEFT JOIN applied_events a ON a.event_id = e.event_id
		  LEFT JOIN hook_event_origin o ON o.event_id = e.event_id
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
			&ev.Author, &payload, &ev.CreatedAt, &ev.Synced, &ev.ViaHook); scanErr != nil {
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

// HookSyncedCursor returns the highest sync_events.rowid the DESIGNATED-host
// synced-dispatch path has processed (MTIX-52). It is independent of
// HookCursor so the local dispatch advancing past synced events does not hide
// them from the synced path.
func (s *Store) HookSyncedCursor(ctx context.Context) (int64, error) {
	var c int64
	err := s.readDB.QueryRowContext(ctx,
		`SELECT cursor FROM hook_synced_cursor WHERE id = 1`).Scan(&c)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read hook synced cursor: %w", err)
	}
	return c, nil
}

// AdvanceHookSyncedCursor moves the synced-dispatch watermark forward to seq
// (monotonic; a lower seq never rewinds it).
func (s *Store) AdvanceHookSyncedCursor(ctx context.Context, seq int64) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hook_synced_cursor (id, cursor) VALUES (1, ?)
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

// HookLogEntry is one row of the hook-firing audit log (FR-19.7).
type HookLogEntry struct {
	Hook    string
	NodeID  string
	Event   string
	Adapter string
	Outcome string
	Detail  string
	FiredAt string
}

// WriteHookLog appends a hook-firing outcome to the audit log (stamped now).
func (s *Store) WriteHookLog(ctx context.Context, e HookLogEntry) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hook_log (hook_name, node_id, event_name, adapter, outcome, detail, fired_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.Hook, e.NodeID, e.Event, e.Adapter, e.Outcome, e.Detail,
			time.Now().UTC().Format(time.RFC3339))
		return err
	})
}

// ReadHookLog returns the most recent hook-firing entries, newest first.
func (s *Store) ReadHookLog(ctx context.Context, limit int) ([]HookLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT hook_name, node_id, event_name, adapter, outcome, COALESCE(detail, ''), fired_at
		  FROM hook_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read hook log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HookLogEntry
	for rows.Next() {
		var e HookLogEntry
		if scanErr := rows.Scan(&e.Hook, &e.NodeID, &e.Event, &e.Adapter, &e.Outcome, &e.Detail, &e.FiredAt); scanErr != nil {
			return nil, fmt.Errorf("scan hook log: %w", scanErr)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HookFiringCount counts DELIVERED firings of hookName on nodeID at or after the
// RFC3339 timestamp since — the input to the per-node rate limit (FR-19.6).
func (s *Store) HookFiringCount(ctx context.Context, hookName, nodeID, since string) (int, error) {
	var n int
	err := s.readDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM hook_log
		 WHERE hook_name = ? AND node_id = ? AND fired_at >= ? AND outcome = 'delivered'`,
		hookName, nodeID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("hook firing count: %w", err)
	}
	return n, nil
}
