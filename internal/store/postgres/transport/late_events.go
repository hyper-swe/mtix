// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Hub reads for the late-event sweep of `mtix sync pull` (MTIX-95.5;
// ADR-006 D5, review F-43; FR-18).
//
// PullEvents serves lamport_clock > cursor, and the Lamport clock is
// stamped by the pushing client. An offline writer's later push carries
// clocks below busier peers' cursors, so those peers never receive it
// through the cursor. After its cursor loop a pull therefore lists hub
// event ids, diffs them against the ids it holds, and fetches the missing
// events by id:
//
//   - ListEventIDsSince lists ids in (created_at, event_id) keyset order
//     from a start position. The sweep uses it on every pull, from the
//     previous sweep's hub time minus an overlap, and once, from the zero
//     time, for the full-history diff of a store that has never swept.
//     The order is a stable keyset, not a causal order (one push gives its
//     events one created_at); the caller applies what it recovers in
//     Lamport order. Migration 014 indexes created_at.
//   - FetchEventsByID returns the full events for a set of ids.
//
// Every listing page carries the hub's now(), read in the same statement,
// so the caller can time its sweep window by the hub's clock alone and
// never by its own (created_at is the hub's transaction start time).

// listEventIDsSinceSQL lists one page of hub event ids in (created_at,
// event_id) order, strictly after the keyset position ($1, $2), with the
// hub's clock. A first page passes the window start and an empty event id,
// so ids created exactly at the start are included. created_at >= $1 lets
// the planner start the scan on idx_sync_events_created_at (migration 014).
// The LEFT JOIN against a one-row relation returns the hub clock even when
// the page is empty (event_id and created_at are then NULL). $3 is the
// page size plus one, to detect a further page.
const listEventIDsSinceSQL = `
SELECT now(), page.event_id, page.created_at
FROM (SELECT 1) AS one
LEFT JOIN (
    SELECT event_id, created_at
    FROM sync_events
    WHERE created_at >= $1
      AND (created_at, event_id) > ($1, $2)
    ORDER BY created_at, event_id
    LIMIT $3
) AS page ON true
ORDER BY page.created_at, page.event_id`

// fetchEventsByIDSQL returns the full hub rows for the ids in $1 (a text
// array), in pull order (Lamport clock, then event id). Ids the hub does
// not hold are simply absent. Served by the primary key. It selects the
// same columns in the same order as pullEventsOnce, so both decode rows
// with scanHubEvent.
const fetchEventsByIDSQL = `
SELECT event_id, project_prefix, node_id, uid, op_type, payload,
       wall_clock_ts, lamport_clock, vector_clock,
       author_id, author_machine_hash, created_at
FROM sync_events
WHERE event_id = ANY($1)
ORDER BY lamport_clock, event_id`

// EventIDCursor is a keyset position in a hub event-id listing: the
// created_at and event_id of the last id listed. The zero value lists the
// whole history (created_at is never before year 1).
type EventIDCursor struct {
	CreatedAt time.Time
	EventID   string
}

// EventIDPage is one page of hub event ids (MTIX-95.5).
type EventIDPage struct {
	// HubNow is the hub's now() for the statement that listed this page.
	HubNow time.Time
	// IDs are the listed event ids, in listing order.
	IDs []string
	// Next is the keyset position after the last id; pass it back to list
	// the next page. It is the zero value when IDs is empty.
	Next EventIDCursor
	// More is true when at least one further id follows this page.
	More bool
}

// ListEventIDsSince returns up to limit hub event ids created at or after
// after.CreatedAt, in (created_at, event_id) order, starting strictly after
// the keyset position after (MTIX-95.5). The first page of a window passes
// the window start and an empty EventID; later pages pass the previous
// page's Next. The page always carries the hub's clock, even when empty.
//
// Wrapped in the same retry envelope as PullEvents; a read, so no
// validation applies.
func (p *Pool) ListEventIDsSince(ctx context.Context, after EventIDCursor, limit int) (EventIDPage, error) {
	if err := p.checkLateEventRead(limit); err != nil {
		return EventIDPage{}, fmt.Errorf("ListEventIDsSince: %w", err)
	}
	page, err := p.listEventIDs(ctx, after, limit)
	if err != nil {
		return EventIDPage{}, fmt.Errorf("ListEventIDsSince: %w", err)
	}
	return page, nil
}

// FetchEventsByID returns the hub events whose ids are in ids, in pull
// order (Lamport clock, then event id), in the shape PullEvents returns
// (MTIX-95.5). Ids the hub does not hold are absent from the result. An
// empty ids returns nothing without a query.
func (p *Pool) FetchEventsByID(ctx context.Context, ids []string) ([]*model.SyncEvent, error) {
	if p == nil || p.p == nil {
		return nil, fmt.Errorf("FetchEventsByID: pool not open")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var events []*model.SyncEvent
	err := retryWithBackoff(ctx, DefaultRetryConfig(), func(ctx context.Context) error {
		evs, opErr := p.fetchEventsByIDOnce(ctx, ids)
		if opErr != nil {
			return opErr
		}
		events = evs
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("FetchEventsByID: %w", err)
	}
	return events, nil
}

// checkLateEventRead rejects a closed pool or a non-positive page size
// before any Postgres round trip.
func (p *Pool) checkLateEventRead(limit int) error {
	if limit <= 0 {
		return fmt.Errorf("limit must be > 0")
	}
	if p == nil || p.p == nil {
		return fmt.Errorf("pool not open")
	}
	return nil
}

// listEventIDs runs listEventIDsSinceSQL under the retry envelope and folds
// its rows into a page of at most limit ids.
func (p *Pool) listEventIDs(ctx context.Context, after EventIDCursor, limit int) (EventIDPage, error) {
	var page EventIDPage
	err := retryWithBackoff(ctx, DefaultRetryConfig(), func(ctx context.Context) error {
		pg, opErr := p.listEventIDsOnce(ctx, after, limit)
		if opErr != nil {
			return opErr
		}
		page = pg
		return nil
	})
	return page, err
}

// listEventIDsOnce runs one listing statement. Its rows are (now(),
// event_id, created_at); a single row with a NULL event_id means an empty
// page. A row past limit only sets More.
func (p *Pool) listEventIDsOnce(ctx context.Context, after EventIDCursor, limit int) (EventIDPage, error) {
	rows, err := p.p.Query(ctx, listEventIDsSinceSQL, after.CreatedAt, after.EventID, limit+1)
	if err != nil {
		return EventIDPage{}, err
	}
	defer rows.Close()

	var page EventIDPage
	for rows.Next() {
		var id *string
		var createdAt *time.Time
		if err := rows.Scan(&page.HubNow, &id, &createdAt); err != nil {
			return EventIDPage{}, err
		}
		if id == nil || createdAt == nil {
			continue
		}
		if len(page.IDs) == limit {
			page.More = true
			continue
		}
		page.IDs = append(page.IDs, *id)
		page.Next = EventIDCursor{CreatedAt: *createdAt, EventID: *id}
	}
	if err := rows.Err(); err != nil {
		return EventIDPage{}, err
	}
	if page.HubNow.IsZero() {
		return EventIDPage{}, fmt.Errorf("hub returned no clock reading")
	}
	return page, nil
}

// fetchEventsByIDOnce runs fetchEventsByIDSQL once.
func (p *Pool) fetchEventsByIDOnce(ctx context.Context, ids []string) ([]*model.SyncEvent, error) {
	rows, err := p.p.Query(ctx, fetchEventsByIDSQL, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*model.SyncEvent, 0, len(ids))
	for rows.Next() {
		e, err := scanHubEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
