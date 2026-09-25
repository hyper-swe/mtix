// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Data access for held push events (MTIX-95.12, review F-15). `mtix sync
// push` validates each pending event before it sends a batch; an event the
// hub would refuse, or one in the subtree of a held task creation, is held
// in sync_quarantine with source push instead of failing the batch. The
// rules live with the push (cmd/mtix/sync_push_hold*.go and
// internal/sync/validator); this file only stores and reads. A held event
// keeps its sync_events row, still pending: push leaves it out of its
// pending reads, so it cannot block the queue, and releases it by deleting
// its row once the hold ends.

// QuarantineSourcePush is the sync_quarantine source of an own event that
// push holds back.
const QuarantineSourcePush = "push"

// HeldPushEvent is one held push event: its event id, the node (the number
// the event names), op, payload and Lamport clock of its sync_events row
// (empty or 0 when the row is missing), its reason, and the task it is
// about (PushSubject).
type HeldPushEvent struct {
	EventID, NodeID, OpType, Reason, Payload string
	Lamport                                  int64
	PushSubject
}

// Now returns the store's clock reading in UTC: push uses it as the
// reference time of its per-event validation and to stamp held events.
func (s *Store) Now() time.Time {
	return s.clock().UTC()
}

// HoldPushEvents records each of holds (source set to push here) in
// sync_quarantine in one transaction, stamped with the store's clock and
// cliVersion. An event already held only gets one more attempt counted
// (QuarantineEvent); its reason stays as first recorded.
func (s *Store) HoldPushEvents(ctx context.Context, holds []QuarantinedEvent, cliVersion string) error {
	if len(holds) == 0 {
		return nil
	}
	at := s.Now().Format(time.RFC3339Nano)
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, q := range holds {
			q.Source, q.FirstSeen, q.LastAttempt, q.CLIVersion = QuarantineSourcePush, at, at, cliVersion
			if err := QuarantineEvent(ctx, tx, q); err != nil {
				return fmt.Errorf("hold push event: %w", err)
			}
		}
		return nil
	})
}

// CountHeldPushEvents returns how many own events push holds, for `mtix
// sync status` and `mtix sync doctor`.
func (s *Store) CountHeldPushEvents(ctx context.Context) (int, error) {
	var n int
	// Every held push event.
	if err := s.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_quarantine WHERE source = 'push'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count held push events: %w", err)
	}
	return n, nil
}

// HeldPushEvents returns up to limit held push events (every one when limit
// is negative) in the order push would have sent them (Lamport clock, then
// event id), in one query: the node, op, payload, Lamport clock and uid of
// each come from its sync_events row, and the current number of the task it
// is about from the node whose uid is the event's uid (idx_nodes_uid; a
// soft-deleted node counts), with, for a held creation, the task it created
// found as PushSubject says. LEFT JOINs still list a hold whose rows are
// missing.
func (s *Store) HeldPushEvents(ctx context.Context, limit int) ([]HeldPushEvent, error) {
	// The held push events in queue order, with their event rows and the
	// current number of the task each is about; for a creation, also the
	// task it created (n by uid, else s by the event's own id for an event
	// without a uid, else f by the number the event names).
	rows, err := s.Query(ctx, `
		SELECT q.event_id, COALESCE(e.node_id, ''), COALESCE(e.op_type, ''), q.reason,
		       COALESCE(e.payload, ''), COALESCE(e.lamport_clock, 0), COALESCE(e.uid, ''), COALESCE(n.id, ''),
		       CASE WHEN e.op_type = 'create_node' THEN COALESCE(n.id, s.id, f.id, '') ELSE '' END,
		       CASE WHEN e.op_type = 'create_node' THEN COALESCE(n.uid, s.uid, f.uid, '') ELSE '' END
		FROM sync_quarantine q
		LEFT JOIN sync_events e ON e.event_id = q.event_id
		LEFT JOIN nodes n ON n.uid = e.uid AND n.uid IS NOT NULL AND n.uid <> ''
		LEFT JOIN nodes s ON e.op_type = 'create_node' AND n.id IS NULL
		     AND COALESCE(e.uid, '') = '' AND s.uid = e.event_id
		LEFT JOIN nodes f ON e.op_type = 'create_node' AND n.id IS NULL AND s.id IS NULL
		     AND f.id = e.node_id
		WHERE q.source = 'push'
		ORDER BY COALESCE(e.lamport_clock, 0), q.event_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read held push events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HeldPushEvent
	for rows.Next() {
		var h HeldPushEvent
		if err := rows.Scan(&h.EventID, &h.NodeID, &h.OpType, &h.Reason, &h.Payload,
			&h.Lamport, &h.UID, &h.CurrentNodeID, &h.TaskNodeID, &h.TaskUID); err != nil {
			return nil, fmt.Errorf("read held push events: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read held push events: %w", err)
	}
	return out, nil
}

// ReleasePushHolds removes the push holds of eventIDs in one statement
// (MTIX-95.12): a clock hold whose event now passes, and the events of a
// released creation's subtree. The events keep their sync_events rows,
// still pending, so the same push sends them. Only rows with source push
// are touched.
func (s *Store) ReleasePushHolds(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	ids, err := json.Marshal(eventIDs)
	if err != nil {
		return fmt.Errorf("release push holds: %w", err)
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		// Drop the push holds of the events in the bound JSON array, in one
		// statement; their sync_events rows stay.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM sync_quarantine
			WHERE source = 'push' AND event_id IN (SELECT value FROM json_each(?))`, string(ids)); err != nil {
			return fmt.Errorf("release push holds: %w", err)
		}
		return nil
	})
}

// NotePushHoldAttempts counts one more attempt, at the store's clock, for
// every push hold whose reason starts with one of kinds (MTIX-95.12): push
// checks each clock hold and each dependent again at its start and writes
// its releases first, so the holds of those kinds left are the ones it
// kept. One statement per kind, however many holds there are; permanent
// holds and quarantined pulled events are not touched.
func (s *Store) NotePushHoldAttempts(ctx context.Context, kinds []string) error {
	if len(kinds) == 0 {
		return nil
	}
	at := s.Now().Format(time.RFC3339Nano)
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, kind := range kinds {
			// One more attempt of each push hold whose reason starts with kind.
			if _, err := tx.ExecContext(ctx, `
				UPDATE sync_quarantine SET attempts = attempts + 1, last_attempt = ?
				WHERE source = 'push' AND substr(reason, 1, ?) = ?`,
				at, len(kind), kind); err != nil {
				return fmt.Errorf("count attempts of push holds %q: %w", kind, err)
			}
		}
		return nil
	})
}

// SetPushHoldReasons replaces the reason of the push holds in reasons (event
// id to new reason) (MTIX-95.12): push relabels a kept dependent whose
// nearest held creation changed. Attempts, first_seen and the raw event are
// kept; NotePushHoldAttempts counts the attempt.
func (s *Store) SetPushHoldReasons(ctx context.Context, reasons map[string]string) error {
	if len(reasons) == 0 {
		return nil
	}
	ids := make([]string, 0, len(reasons))
	for id := range reasons {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			// New reason for one push hold.
			if _, err := tx.ExecContext(ctx, `
				UPDATE sync_quarantine SET reason = ?
				WHERE event_id = ? AND source = 'push'`, reasons[id], id); err != nil {
				return fmt.Errorf("set reason of push hold %s: %w", id, err)
			}
		}
		return nil
	})
}
