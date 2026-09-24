// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Late-event sweep of `mtix sync pull` (MTIX-95.5; ADR-006 D5, review F-43;
// FR-18).
//
// The pull cursor is a Lamport clock stamped by the pushing client, and the
// hub serves lamport_clock > cursor. A teammate who worked offline pushes
// events stamped below busier peers' cursors, so the cursor loop never
// returns them and replicas silently diverge. After the cursor loop, the
// sweep therefore:
//
//  1. lists the hub event ids created since the previous sweep's hub time
//     minus lateEventSweepOverlap (or, on a store that has never swept, the
//     full hub id history once, paged by event id);
//  2. diffs each page against the ids this store holds, in sync_events
//     (its own events and every mirrored one) or applied_events;
//  3. fetches the missing events by id and applies them in pull order
//     through applyPullBatch, the cursor loop's ingest path. Recovered
//     events are late and low-Lamport by construction, so a recovered
//     claim, unclaim, defer or status change goes through the MTIX-95.10
//     workflow winner rule there and cannot revert newer status;
//  4. records the hub time read by its first listing statement in
//     meta.sync.last_sweep_at.
//
// The window and the recorded time come only from the hub's clock (now()
// in the listing statement), never from this machine's clock. The sweep
// does not move the Lamport cursor. It adds no timer: it runs only inside
// a pull, with one listing query when nothing is missing. The complete fix
// is a hub-assigned sequence (ADR-006 phase 2).

// lateEventSweepOverlap is how far before the previous sweep's hub time the
// next window starts. created_at is the hub transaction's start time, so a
// push that started before a sweep can commit after it with an earlier
// created_at; the overlap must exceed the longest push transaction (each
// statement is bounded by the 10s statement_timeout). 15 minutes is ample.
const lateEventSweepOverlap = 15 * time.Minute

// lateEventHub is the hub surface the sweep reads; *transport.Pool
// implements it.
type lateEventHub interface {
	ListEventIDsSince(ctx context.Context, after transport.EventIDCursor, limit int) (transport.EventIDPage, error)
	ListAllEventIDs(ctx context.Context, afterID string, limit int) (transport.EventIDPage, error)
	FetchEventsByID(ctx context.Context, ids []string) ([]*model.SyncEvent, error)
}

// lateEventSweep is the outcome of one sweep.
type lateEventSweep struct {
	// Recovered is the number of late events fetched and applied.
	Recovered int
	// FullDiff is true when the sweep compared the full hub id history
	// (the first sweep on this store).
	FullDiff bool
}

// sweepWindow says which hub ids a sweep lists: those created since start,
// or, when full is set, every id.
type sweepWindow struct {
	start time.Time
	full  bool
}

// sweepLateEvents runs one late-event sweep after the cursor loop
// (MTIX-95.5). limit is the page size for listing ids, fetching events and
// applying them. meta.sync.last_sweep_at advances only when every
// recovered event has been applied, so a failed sweep is retried from the
// same window by the next pull.
func sweepLateEvents(ctx context.Context, stderr io.Writer, hub lateEventHub,
	st *sqlite.Store, limit int,
) (lateEventSweep, error) {
	window, err := readSweepWindow(ctx, stderr, st)
	if err != nil {
		return lateEventSweep{}, err
	}
	if window.full {
		fmt.Fprintln(stderr,
			"late-event sweep: first sweep on this store; comparing the full hub event history once")
	}
	missing, hubNow, err := listMissingEventIDs(ctx, hub, st, window, limit)
	if err != nil {
		return lateEventSweep{}, err
	}
	recovered, err := applyLateEvents(ctx, hub, st, missing, limit)
	out := lateEventSweep{Recovered: recovered, FullDiff: window.full}
	if err != nil {
		return out, err
	}
	if err := writeLastSweepAt(ctx, st, hubNow); err != nil {
		return out, err
	}
	return out, nil
}

// printLateEventSweep reports the sweep on stdout: always after a full
// diff, otherwise only when it recovered something.
func printLateEventSweep(w io.Writer, s lateEventSweep) {
	switch {
	case s.FullDiff:
		fmt.Fprintf(w, "late-event sweep (first run, full hub history): %d late events recovered\n",
			s.Recovered)
	case s.Recovered > 0:
		fmt.Fprintf(w, "late-event sweep: %d late events recovered\n", s.Recovered)
	}
}

// readSweepWindow derives this sweep's window from meta.sync.last_sweep_at:
// the previous sweep's hub time minus lateEventSweepOverlap. An empty or
// absent value means the store has never swept, so the window is the full
// hub history. So is a value that is not an RFC 3339 time, after a warning:
// a full diff is always correct, only slower, and it rewrites the value.
func readSweepWindow(ctx context.Context, stderr io.Writer, st *sqlite.Store) (sweepWindow, error) {
	var raw string
	err := st.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.last_sweep_at'`,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && raw == "") {
		return sweepWindow{full: true}, nil
	}
	if err != nil {
		return sweepWindow{}, fmt.Errorf("read meta.sync.last_sweep_at: %w", err)
	}
	last, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		fmt.Fprintf(stderr,
			"WARN: late-event sweep: meta.sync.last_sweep_at %q is not a time (%s); comparing the full hub event history instead\n",
			raw, err)
		return sweepWindow{full: true}, nil
	}
	return sweepWindow{start: last.Add(-lateEventSweepOverlap)}, nil
}

// listMissingEventIDs pages through the window's hub ids and returns the
// ones this store does not hold, plus the hub time read by the first
// listing statement (the value the next window is measured from).
func listMissingEventIDs(ctx context.Context, hub lateEventHub, st *sqlite.Store,
	window sweepWindow, limit int,
) ([]string, time.Time, error) {
	var missing []string
	var hubNow time.Time
	cursor := transport.EventIDCursor{CreatedAt: window.start}
	for {
		page, err := listEventIDPage(ctx, hub, window, cursor, limit)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("list hub event ids: %w", err)
		}
		if hubNow.IsZero() {
			if page.HubNow.IsZero() {
				return nil, time.Time{}, fmt.Errorf("list hub event ids: hub returned no clock reading")
			}
			hubNow = page.HubNow
		}
		pageMissing, err := missingLocalEventIDs(ctx, st, page.IDs)
		if err != nil {
			return nil, time.Time{}, err
		}
		missing = append(missing, pageMissing...)
		if !page.More {
			return missing, hubNow, nil
		}
		if len(page.IDs) == 0 {
			return nil, time.Time{}, fmt.Errorf("list hub event ids: hub reported more ids after an empty page")
		}
		cursor = page.Next
	}
}

// listEventIDPage lists one page of the window: by created_at from the
// window start, or, for a full diff, the whole history by event id.
func listEventIDPage(ctx context.Context, hub lateEventHub, window sweepWindow,
	cursor transport.EventIDCursor, limit int,
) (transport.EventIDPage, error) {
	if window.full {
		return hub.ListAllEventIDs(ctx, cursor.EventID, limit)
	}
	return hub.ListEventIDsSince(ctx, cursor, limit)
}

// missingLocalEventIDs returns, in the given order, the ids this store
// holds in neither sync_events (its own events and every mirrored one) nor
// applied_events.
func missingLocalEventIDs(ctx context.Context, st *sqlite.Store, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	page, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("encode hub id page: %w", err)
	}
	// Expand the page, passed as ONE JSON-array parameter, with json_each,
	// so the SQL text is fixed whatever the page size; keep the ids found in
	// neither local table (both keyed by event_id), in page order.
	rows, err := st.Query(ctx, `
		SELECT j.value FROM json_each(?) AS j
		WHERE NOT EXISTS (SELECT 1 FROM sync_events s WHERE s.event_id = j.value)
		  AND NOT EXISTS (SELECT 1 FROM applied_events a WHERE a.event_id = j.value)
		ORDER BY j.key`, string(page))
	if err != nil {
		return nil, fmt.Errorf("diff hub ids against local events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var missing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("diff hub ids against local events: %w", err)
		}
		missing = append(missing, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("diff hub ids against local events: %w", err)
	}
	return missing, nil
}

// applyLateEvents fetches the missing events and applies them in pull
// order (Lamport clock, then event id), in batches of limit, through
// applyPullBatch. Returns how many were applied before any error.
func applyLateEvents(ctx context.Context, hub lateEventHub, st *sqlite.Store,
	missing []string, limit int,
) (int, error) {
	events, err := fetchLateEvents(ctx, hub, missing, limit)
	if err != nil {
		return 0, err
	}
	applied := 0
	for start := 0; start < len(events); start += limit {
		batch := events[start:min(start+limit, len(events))]
		if err := applyPullBatch(ctx, st, batch); err != nil {
			return applied, fmt.Errorf("apply late events: %w", err)
		}
		applied += len(batch)
	}
	return applied, nil
}

// fetchLateEvents fetches ids from the hub in chunks of limit and returns
// the events sorted into pull order across all chunks, so a recovered
// create applies before a recovered edit of the same node.
func fetchLateEvents(ctx context.Context, hub lateEventHub, ids []string, limit int) ([]*model.SyncEvent, error) {
	var events []*model.SyncEvent
	for start := 0; start < len(ids); start += limit {
		chunk, err := hub.FetchEventsByID(ctx, ids[start:min(start+limit, len(ids))])
		if err != nil {
			return nil, fmt.Errorf("fetch late events: %w", err)
		}
		events = append(events, chunk...)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].LamportClock != events[j].LamportClock {
			return events[i].LamportClock < events[j].LamportClock
		}
		return events[i].EventID < events[j].EventID
	})
	return events, nil
}

// writeLastSweepAt records the sweep's hub time in meta.sync.last_sweep_at
// as RFC 3339 UTC. It upserts, so a store whose key is missing records it.
func writeLastSweepAt(ctx context.Context, st *sqlite.Store, hubNow time.Time) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		// Insert or replace the one sentinel row keyed by meta.key.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO meta (key, value) VALUES ('meta.sync.last_sweep_at', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			hubNow.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("write meta.sync.last_sweep_at: %w", err)
		}
		return nil
	})
}
