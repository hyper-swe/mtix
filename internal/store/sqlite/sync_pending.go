// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// The pending-queue projection: every read of sync_events that feeds a push
// selects these columns and decodes them with scanPushEvents, so there is
// exactly ONE projection of an event on its way to the hub.
//
// This lives in the store rather than in cmd/mtix so there is exactly ONE
// pending-queue projection. It used to be duplicated: cmd/mtix had the
// production copy and e2e had a hand-maintained "mirror" that had drifted
// to include a column production omitted (MTIX-91). The harness was more
// correct than the shipping code, so e2e exercised a path the CLI never
// took and the defect passed every test. Every caller now shares this: the
// CLI's batch read, the e2e harness, and the re-read of held push events
// (MTIX-95.12). Do not re-inline it.
//
// uid is load-bearing on the wire, not decoration. The hub resolves a
// registered create's EFFECTIVE uid as its stored uid, falling back to its
// event_id when that is NULL (transport/registry.go). Dropping uid here
// made every hub row NULL, so the effective uid was always an event_id and
// a re-emitted create — which carries the node's real, stable uid — could
// never match it. The hub then classified the same logical node as a
// DIFFERENT one and demanded a renumber, making the ADR-003 §6/§9
// same-node no-op unreachable for every pushed create.

// ReadPendingEvents returns up to limit events awaiting push from the start
// of the queue, in (lamport, event id) order, leaving out the events push
// holds (sync_quarantine source push, MTIX-95.12). It is
// ReadPendingEventsAfter from the zero position, so the CLI and the e2e
// harness read the queue the same way (MTIX-91).
func (s *Store) ReadPendingEvents(ctx context.Context, limit int) ([]*model.SyncEvent, error) {
	return s.ReadPendingEventsAfter(ctx, 0, "", limit)
}

// ReadPendingEventsAfter returns up to limit pending events from sync_events
// after the position (afterLamport, afterEventID), in (lamport, event id)
// order, leaving out the events push holds (sync_quarantine source push,
// MTIX-95.12): a held event would otherwise stay at the head of the queue
// and, once a batch was full of them, stop every later event from being
// read. The zero position (0, "") is the start of the queue. Each event
// carries its uid (MTIX-91). Reads via readDB — no write tx needed.
func (s *Store) ReadPendingEventsAfter(ctx context.Context, afterLamport int64, afterEventID string, limit int) ([]*model.SyncEvent, error) {
	// Pending events after the position in Lamport order, minus the held
	// ones (one primary-key probe of sync_quarantine per event). The bare
	// lamport_clock >= ? bound lets the pending index seek to the position
	// instead of scanning from the head of the queue. uid is selected for
	// the hub's same-logical-node no-op (MTIX-91).
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT event_id, project_prefix, node_id, uid, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending'
		  AND lamport_clock >= ? AND (lamport_clock > ? OR event_id > ?)
		  AND NOT EXISTS (SELECT 1 FROM sync_quarantine q
		                  WHERE q.event_id = sync_events.event_id AND q.source = 'push')
		ORDER BY lamport_clock ASC, event_id ASC
		LIMIT ?`, afterLamport, afterLamport, afterEventID, limit)
	if err != nil {
		return nil, fmt.Errorf("read pending events: %w", err)
	}
	events, err := scanPushEvents(rows, limit)
	if err != nil {
		return nil, fmt.Errorf("read pending events: %w", err)
	}
	return events, nil
}

// ReadPendingEventsByID returns the pending events among ids, read from
// sync_events in one query, in (lamport, event id) order. Push re-reads its
// held events this way to validate them again before releasing a hold
// (MTIX-95.12); each event carries its uid like every other push read
// (MTIX-91). An id with no pending event is left out. Reads via readDB.
func (s *Store) ReadPendingEventsByID(ctx context.Context, ids []string) ([]*model.SyncEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("read pending events by id: %w", err)
	}
	// The pending events whose ids are in the bound JSON array, with the
	// push projection columns (uid included, MTIX-91).
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT event_id, project_prefix, node_id, uid, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending' AND event_id IN (SELECT value FROM json_each(?))
		ORDER BY lamport_clock ASC, event_id ASC`, string(list))
	if err != nil {
		return nil, fmt.Errorf("read pending events by id: %w", err)
	}
	events, err := scanPushEvents(rows, len(ids))
	if err != nil {
		return nil, fmt.Errorf("read pending events by id: %w", err)
	}
	return events, nil
}

// scanPushEvents decodes the events of rows, which select the push
// projection (event_id, project_prefix, node_id, uid, op_type, payload,
// wall_clock_ts, lamport_clock, vector_clock, author_id,
// author_machine_hash), and closes rows. capHint sizes the result.
func scanPushEvents(rows *sql.Rows, capHint int) ([]*model.SyncEvent, error) {
	defer func() { _ = rows.Close() }()
	out := make([]*model.SyncEvent, 0, capHint)
	for rows.Next() {
		var e model.SyncEvent
		var opType, payload, vc string
		var uid sql.NullString
		if err := rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &uid, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash,
		); err != nil {
			return nil, fmt.Errorf("scan pending event: %w", err)
		}
		// A NULL uid is legitimate for a pre-ADR-003 row that BackfillUIDs
		// has not reached; apply falls back to node_id in that case.
		e.UID = uid.String
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		if err := json.Unmarshal([]byte(vc), &e.VectorClock); err != nil {
			return nil, fmt.Errorf("decode VC for %s: %w", e.EventID, err)
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending events: %w", err)
	}
	return out, nil
}
