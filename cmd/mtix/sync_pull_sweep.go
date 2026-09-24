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
//  1. lists, in (created_at, event_id) keyset pages, the hub event ids
//     created since the previous sweep's hub time minus
//     lateEventSweepOverlap (or, on a store that has never swept, the full
//     hub id history once). created_at is the push transaction's start
//     time on the hub, so an event's page is never after the page of an
//     event pushed once it had been received (a node's create comes no
//     later than its edits from other clients);
//  2. diffs each page against the ids this store holds, in sync_events
//     (its own events and every mirrored one) or applied_events;
//  3. fetches the page's missing events by id and applies them in pull
//     order through applyPullBatch, the cursor loop's ingest path, before
//     it lists the next page. Recovered events are late and low-Lamport by
//     construction, so a recovered claim, unclaim, defer or status change
//     goes through the MTIX-95.10 workflow winner rule there and cannot
//     revert newer status;
//  4. records the hub time read before its first page in
//     meta.sync.last_sweep_at.
//
// A sweep is resumable, which matters most for the one-time full diff of a
// large hub (a daemon pull has a 60s deadline). After a page's missing
// events are applied, and another page follows, the sweep saves its
// progress (the last listed event id with its created_at, and the hub time
// read before its first page) in meta.sync.sweep_after_id,
// meta.sync.sweep_after_created_at and meta.sync.sweep_started_at. A pull
// that fails or times out part-way keeps that progress; the next pull
// resumes after the saved position with the saved start time. On
// completion the start time becomes meta.sync.last_sweep_at and the
// progress is cleared. Events created while a sweep was interrupted are
// covered by the next window, which starts 15 minutes before that start
// time.
//
// The window and the recorded time come only from the hub's clock (now()
// in the listing statement), never from this machine's clock. The sweep
// does not move the Lamport cursor. It adds no timer: it runs only inside
// a pull, with one listing query when nothing is missing. The complete fix
// is a hub-assigned sequence (ADR-006 phase 2).

// lateEventSweepOverlap is how far before the previous sweep's hub time the
// next window starts. created_at is the hub transaction's start time, so a
// push that started before a sweep can commit after it with an earlier
// created_at. The overlap covers push transactions of normal length, which
// take seconds. It is not a bound: the 10s statement_timeout limits each
// statement, not the push transaction, so a stalled push transaction can
// stay open longer than 15 minutes, and an event it then commits is missed
// by later windowed sweeps. Bounding the push transaction is a separate
// follow-up.
const lateEventSweepOverlap = 15 * time.Minute

// Local meta keys of the sweep, all seeded empty by the SQLite schema
// (MTIX-95.5). Empty last_sweep_at means no sweep has completed, so the
// next sweep is a full diff; empty sweep_after_id means no sweep is
// part-way done.
const (
	lastSweepAtKey         = "meta.sync.last_sweep_at"
	sweepAfterIDKey        = "meta.sync.sweep_after_id"
	sweepAfterCreatedAtKey = "meta.sync.sweep_after_created_at"
	sweepStartedAtKey      = "meta.sync.sweep_started_at"
)

// lateEventHub is the hub surface the sweep reads; *transport.Pool
// implements it.
type lateEventHub interface {
	ListEventIDsSince(ctx context.Context, after transport.EventIDCursor, limit int) (transport.EventIDPage, error)
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

// sweepWindow says which hub ids a sweep lists: those after the keyset
// position from (a window start, or the zero position for a full diff).
// full marks the full-history diff. A sweep that resumes saved progress
// starts from the saved position and keeps its original startedAt.
type sweepWindow struct {
	from      transport.EventIDCursor
	full      bool
	resuming  bool
	startedAt time.Time
}

// sweepPass is the running result of one sweep's pages.
type sweepPass struct {
	// startedAt is the hub time read before the first page (for a resumed
	// full diff, the saved one): the value the next window is measured from.
	startedAt time.Time
	// recovered counts the late events applied; compared and pages count
	// the hub ids listed and the listing statements run by this pull.
	recovered, compared, pages int
}

// sweepLateEvents runs one late-event sweep after the cursor loop
// (MTIX-95.5). limit is the page size for listing ids, fetching events and
// applying them. meta.sync.last_sweep_at advances only when the whole window
// has been listed and every recovered event applied; a failed sweep resumes
// from its saved progress on the next pull.
func sweepLateEvents(ctx context.Context, stderr io.Writer, hub lateEventHub,
	st *sqlite.Store, limit int,
) (lateEventSweep, error) {
	window, err := readSweepWindow(ctx, stderr, st)
	if err != nil {
		return lateEventSweep{}, err
	}
	announceSweep(stderr, window)
	pass, err := sweepPages(ctx, hub, st, window, limit)
	out := lateEventSweep{Recovered: pass.recovered, FullDiff: window.full}
	if err != nil {
		return out, err
	}
	if window.full {
		fmt.Fprintf(stderr, "late-event sweep: compared %d hub event ids in %d pages\n",
			pass.compared, pass.pages)
	}
	if err := finishLateEventSweep(ctx, st, pass.startedAt); err != nil {
		return out, err
	}
	return out, nil
}

// announceSweep tells stderr that a sweep resumes, or that a full diff
// starts.
func announceSweep(stderr io.Writer, window sweepWindow) {
	started := window.startedAt.UTC().Format(time.RFC3339Nano)
	switch {
	case window.resuming && window.full:
		fmt.Fprintf(stderr,
			"late-event sweep: resuming the full hub event comparison after %s (started at hub time %s)\n",
			window.from.EventID, started)
	case window.resuming:
		fmt.Fprintf(stderr, "late-event sweep: resuming after %s (started at hub time %s)\n",
			window.from.EventID, started)
	case window.full:
		fmt.Fprintln(stderr,
			"late-event sweep: first sweep on this store; comparing the full hub event history once")
	}
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

// readSweepWindow derives this sweep's window. meta.sync.last_sweep_at gives
// the previous sweep's hub time; the window starts lateEventSweepOverlap
// before it. An empty or absent value means no sweep has completed, so the
// sweep is a full diff from the zero position. So is a value that is not an
// RFC 3339 time, after a warning: a full diff is always correct, only
// slower, and it rewrites the value. Saved progress of an interrupted sweep
// then moves the start to the saved position (resumeSweepProgress).
func readSweepWindow(ctx context.Context, stderr io.Writer, st *sqlite.Store) (sweepWindow, error) {
	raw, err := readSweepMeta(ctx, st, lastSweepAtKey)
	if err != nil {
		return sweepWindow{}, err
	}
	window := sweepWindow{full: true}
	if raw != "" {
		last, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			fmt.Fprintf(stderr,
				"WARN: late-event sweep: meta.sync.last_sweep_at %q is not a time (%s); comparing the full hub event history instead\n",
				raw, parseErr)
		} else {
			window = sweepWindow{from: transport.EventIDCursor{CreatedAt: last.Add(-lateEventSweepOverlap)}}
		}
	}
	return resumeSweepProgress(ctx, stderr, st, window)
}

// resumeSweepProgress returns window resumed from the saved progress of an
// interrupted sweep, or window unchanged when there is none. A saved time
// that is not an RFC 3339 time is reported and the sweep restarts from the
// window's own start.
func resumeSweepProgress(ctx context.Context, stderr io.Writer, st *sqlite.Store,
	window sweepWindow,
) (sweepWindow, error) {
	afterID, err := readSweepMeta(ctx, st, sweepAfterIDKey)
	if err != nil || afterID == "" {
		return window, err
	}
	times := make(map[string]time.Time, 2)
	for _, key := range []string{sweepAfterCreatedAtKey, sweepStartedAtKey} {
		raw, readErr := readSweepMeta(ctx, st, key)
		if readErr != nil {
			return window, readErr
		}
		at, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			fmt.Fprintf(stderr,
				"WARN: late-event sweep: %s %q is not a time (%s); restarting the sweep from its start\n",
				key, raw, parseErr)
			return window, nil
		}
		times[key] = at
	}
	window.from = transport.EventIDCursor{CreatedAt: times[sweepAfterCreatedAtKey], EventID: afterID}
	window.resuming = true
	window.startedAt = times[sweepStartedAtKey]
	return window, nil
}

// readSweepMeta returns one sweep meta value, or "" when its row is absent.
func readSweepMeta(ctx context.Context, st *sqlite.Store, key string) (string, error) {
	var v string
	err := st.QueryRow(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	return v, nil
}

// sweepPages lists the window's hub ids one page (limit ids) at a time and,
// before listing the next page, applies the page's missing events. When
// another page follows, it then saves its progress (never before the apply),
// so a pull stopped part-way resumes after the last page it applied; a
// single-page sweep writes nothing until it finishes.
func sweepPages(ctx context.Context, hub lateEventHub, st *sqlite.Store,
	window sweepWindow, limit int,
) (sweepPass, error) {
	pass := sweepPass{startedAt: window.startedAt}
	cursor := window.from
	for {
		page, err := hub.ListEventIDsSince(ctx, cursor, limit)
		if err != nil {
			return pass, fmt.Errorf("list hub event ids: %w", err)
		}
		if pass.startedAt.IsZero() {
			if page.HubNow.IsZero() {
				return pass, fmt.Errorf("list hub event ids: hub returned no clock reading")
			}
			pass.startedAt = page.HubNow
		}
		pass.pages++
		pass.compared += len(page.IDs)
		n, err := recoverPage(ctx, hub, st, page.IDs, limit)
		pass.recovered += n
		if err != nil {
			return pass, err
		}
		if !page.More {
			return pass, nil
		}
		if len(page.IDs) == 0 {
			return pass, fmt.Errorf("list hub event ids: hub reported more ids after an empty page")
		}
		if err := saveSweepProgress(ctx, st, page.Next, pass.startedAt); err != nil {
			return pass, err
		}
		cursor = page.Next
	}
}

// recoverPage applies the events of one listed page that this store does
// not hold and returns how many it applied.
func recoverPage(ctx context.Context, hub lateEventHub, st *sqlite.Store,
	ids []string, limit int,
) (int, error) {
	missing, err := missingLocalEventIDs(ctx, st, ids)
	if err != nil {
		return 0, err
	}
	return applyLateEvents(ctx, hub, st, missing, limit)
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

// saveSweepProgress records, after a page has been applied, the keyset
// position of the last listed id and the hub time read before the sweep's
// first page, in one transaction.
func saveSweepProgress(ctx context.Context, st *sqlite.Store,
	after transport.EventIDCursor, startedAt time.Time,
) error {
	return writeSweepMeta(ctx, st, map[string]string{
		sweepAfterIDKey:        after.EventID,
		sweepAfterCreatedAtKey: after.CreatedAt.UTC().Format(time.RFC3339Nano),
		sweepStartedAtKey:      startedAt.UTC().Format(time.RFC3339Nano),
	})
}

// finishLateEventSweep records a completed sweep: meta.sync.last_sweep_at
// becomes the hub time read before its first page (RFC 3339, UTC), and any
// saved progress is cleared, in one transaction.
func finishLateEventSweep(ctx context.Context, st *sqlite.Store, startedAt time.Time) error {
	return writeSweepMeta(ctx, st, map[string]string{
		lastSweepAtKey:         startedAt.UTC().Format(time.RFC3339Nano),
		sweepAfterIDKey:        "",
		sweepAfterCreatedAtKey: "",
		sweepStartedAtKey:      "",
	})
}

// resetLateEventSweep clears the sweep state (meta.sync.last_sweep_at and
// any saved progress), so the next pull diffs the full hub history from the
// zero position. sync clone calls it: a clone rebuilds the store from the
// hub (MTIX-95.5).
func resetLateEventSweep(ctx context.Context, st *sqlite.Store) error {
	return writeSweepMeta(ctx, st, map[string]string{
		lastSweepAtKey:         "",
		sweepAfterIDKey:        "",
		sweepAfterCreatedAtKey: "",
		sweepStartedAtKey:      "",
	})
}

// writeSweepMeta upserts the given meta values in one transaction, so a
// store whose key rows are missing records them again.
func writeSweepMeta(ctx context.Context, st *sqlite.Store, values map[string]string) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		for key, value := range values {
			// Insert or replace one sentinel row keyed by meta.key.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO meta (key, value) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
				key, value,
			); err != nil {
				return fmt.Errorf("write %s: %w", key, err)
			}
		}
		return nil
	})
}
