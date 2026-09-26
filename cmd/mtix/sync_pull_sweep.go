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
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Late-event sweep of `mtix sync pull` (MTIX-95.5; ADR-006 D5, review F-43;
// FR-18).
//
// The pull cursor is a (lamport_clock, event_id) position whose clock is
// stamped by the pushing client, and the hub serves the events after it
// (MTIX-95.4). A teammate who worked offline pushes events stamped below
// busier peers' cursors, so the cursor loop never returns them and
// replicas silently diverge. After the cursor loop, the sweep therefore:
//
//  1. Listing phase: lists, in (created_at, event_id) keyset pages, the hub
//     event ids created since the previous sweep's hub time minus
//     lateEventSweepOverlap (or, on a store that has never swept, the full
//     hub id history once), diffs each page against the ids this store
//     holds (sync_events, its own events and every mirrored one, or
//     applied_events) or has quarantined (sync_quarantine, MTIX-95.11:
//     the quarantine retries those itself), and stages the missing ids in
//     the local
//     sync_sweep_pending table. It applies nothing: the listing order is
//     not causal (one client's push gives all its events one created_at,
//     and after a clock step-back or a renumbered create an edit can be
//     listed before its node's create).
//  2. Apply phase, once the listing is complete: fetches every staged
//     event by id, sorts them all into pull order (Lamport clock, then
//     event id), which is causal, and applies them through applyPullBatch,
//     the cursor loop's ingest path, removing each id from the staging
//     table in its apply's transaction. A recovered event that fails its
//     checks or its apply is quarantined like one from the cursor loop
//     (MTIX-95.11, sync_pull_quarantine.go) and removed from the staging
//     table in the same transaction. Recovered events are late and
//     low-Lamport by construction, so a recovered claim, unclaim, defer or
//     status change goes through the MTIX-95.10 workflow winner rule there
//     and cannot revert newer status.
//  3. Records the hub time read before its first page in
//     meta.sync.last_sweep_at once nothing is staged.
//
// A sweep is resumable, which matters most for the one-time full diff of a
// large hub (a daemon pull has a 60s deadline). With each page's staged ids
// the listing saves its position (the last listed event id with its
// created_at) and the hub time read before its first page in
// meta.sync.sweep_after_id, meta.sync.sweep_after_created_at and
// meta.sync.sweep_started_at. A pull that fails or times out in the
// listing phase resumes listing after the saved position; one that stops
// in the apply phase keeps the unapplied ids staged, and the next pull
// applies them, still in Lamport order because the lower clocks went
// first. On completion the start time becomes meta.sync.last_sweep_at and
// the progress is cleared. Events created while a sweep was interrupted
// are covered by the next window, which starts 15 minutes before that
// start time.
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

// sweepListing is the result of one sweep's listing phase.
type sweepListing struct {
	// startedAt is the hub time read before the first page (for a resumed
	// sweep, the saved one): the value the next window is measured from.
	startedAt time.Time
	// compared and pages count the hub ids listed and the listing
	// statements run by this pull.
	compared, pages int
}

// sweepLateEvents runs one late-event sweep after the cursor loop
// (MTIX-95.5): the listing phase stages the missing ids, then the apply
// phase applies every staged event in Lamport order, through the ingest
// checks and quarantine of in (MTIX-95.11). limit is the page size for
// listing ids, fetching events and applying them.
// meta.sync.last_sweep_at advances only when the whole window has been
// listed and nothing is left staged; a failed sweep resumes on the next
// pull.
func sweepLateEvents(ctx context.Context, in pullIngest, hub lateEventHub,
	st *sqlite.Store, limit int,
) (lateEventSweep, error) {
	window, err := readSweepWindow(ctx, in.stderr, st)
	if err != nil {
		return lateEventSweep{}, err
	}
	announceSweep(in.stderr, window)
	listing, err := listLateEvents(ctx, hub, st, window, limit)
	out := lateEventSweep{FullDiff: window.full}
	if err != nil {
		return out, err
	}
	if window.full {
		fmt.Fprintf(in.stderr, "late-event sweep: compared %d hub event ids in %d pages\n",
			listing.compared, listing.pages)
	}
	out.Recovered, err = applyStagedLateEvents(ctx, in, hub, st, limit)
	if err != nil {
		return out, err
	}
	recorded, err := finishLateEventSweep(ctx, st, listing.startedAt)
	if err != nil {
		return out, err
	}
	if !recorded {
		fmt.Fprintln(in.stderr,
			"late-event sweep: another pull staged events meanwhile; a later pull applies them and records the sweep")
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

// notePage counts one listed page and, on the sweep's first page, takes its
// hub clock as the sweep's start time.
func (l *sweepListing) notePage(page transport.EventIDPage) error {
	if l.startedAt.IsZero() {
		if page.HubNow.IsZero() {
			return fmt.Errorf("list hub event ids: hub returned no clock reading")
		}
		l.startedAt = page.HubNow
	}
	l.pages++
	l.compared += len(page.IDs)
	return nil
}

// listLateEvents is the listing phase: it lists the window's hub ids one
// page (limit ids) at a time and stages the ids this store does not hold.
// It applies nothing. With a page's staged ids it saves the listing
// position in the same transaction, so a pull stopped part-way resumes
// after the last page it staged; a page with nothing to stage and no page
// after it writes nothing.
func listLateEvents(ctx context.Context, hub lateEventHub, st *sqlite.Store,
	window sweepWindow, limit int,
) (sweepListing, error) {
	listing := sweepListing{startedAt: window.startedAt}
	cursor := window.from
	for {
		page, err := hub.ListEventIDsSince(ctx, cursor, limit)
		if err != nil {
			return listing, fmt.Errorf("list hub event ids: %w", err)
		}
		if noteErr := listing.notePage(page); noteErr != nil {
			return listing, noteErr
		}
		if page.More && len(page.IDs) == 0 {
			return listing, fmt.Errorf("list hub event ids: hub reported more ids after an empty page")
		}
		missing, err := missingLocalEventIDs(ctx, st, page.IDs)
		if err != nil {
			return listing, err
		}
		if len(missing) > 0 || page.More {
			staged := withLamports(missing, page)
			if err := stageLateEvents(ctx, st, staged, page.Next, listing.startedAt); err != nil {
				return listing, err
			}
		}
		if !page.More {
			return listing, nil
		}
		cursor = page.Next
	}
}

// stagedLateEvent is one missing hub event id with its Lamport clock, as
// the listing staged it.
type stagedLateEvent struct {
	id      string
	lamport int64
}

// withLamports pairs each missing id with the Lamport clock the listed page
// carries for it.
func withLamports(missing []string, page transport.EventIDPage) []stagedLateEvent {
	clocks := make(map[string]int64, len(page.IDs))
	for i, id := range page.IDs {
		if i < len(page.Lamports) {
			clocks[id] = page.Lamports[i]
		}
	}
	out := make([]stagedLateEvent, 0, len(missing))
	for _, id := range missing {
		out = append(out, stagedLateEvent{id: id, lamport: clocks[id]})
	}
	return out
}

// stageLateEvents records, in one transaction, the missing ids of a listed
// page with their Lamport clocks in sync_sweep_pending, and the listing
// position after that page with the hub time read before the sweep's first
// page.
func stageLateEvents(ctx context.Context, st *sqlite.Store, staged []stagedLateEvent,
	after transport.EventIDCursor, startedAt time.Time,
) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range staged {
			// Stage one missing hub event id with its Lamport clock; an id
			// staged by an earlier, interrupted pull is already there.
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO sync_sweep_pending (event_id, lamport_clock) VALUES (?, ?)`,
				e.id, e.lamport,
			); err != nil {
				return fmt.Errorf("stage %s: %w", e.id, err)
			}
		}
		return upsertSweepMeta(ctx, tx, map[string]string{
			sweepAfterIDKey:        after.EventID,
			sweepAfterCreatedAtKey: after.CreatedAt.UTC().Format(time.RFC3339Nano),
			sweepStartedAtKey:      startedAt.UTC().Format(time.RFC3339Nano),
		})
	})
}

// missingLocalEventIDs returns, in the given order, the ids this store
// holds in neither sync_events (its own events and every mirrored one) nor
// applied_events, and has not quarantined (sync_quarantine, MTIX-95.11): a
// quarantined event is already stored locally and the quarantine retries
// it on every pull, so the sweep never fetches it again.
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
	// none of the local tables (all keyed by event_id), in page order.
	rows, err := st.Query(ctx, `
		SELECT j.value FROM json_each(?) AS j
		WHERE NOT EXISTS (SELECT 1 FROM sync_events s WHERE s.event_id = j.value)
		  AND NOT EXISTS (SELECT 1 FROM applied_events a WHERE a.event_id = j.value)
		  AND NOT EXISTS (SELECT 1 FROM sync_quarantine q WHERE q.event_id = j.value)
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

// finishLateEventSweep records a completed sweep, in one transaction and
// only when nothing is left staged: meta.sync.last_sweep_at becomes the hub
// time read before its first page (RFC 3339, UTC), and the listing progress
// is cleared. When ids are still staged (another pull staged them after
// this one's apply phase ended), it records nothing and returns false with
// no error: the sweep is not complete, the progress stays, and the next
// pull applies those ids and records the sweep.
func finishLateEventSweep(ctx context.Context, st *sqlite.Store, startedAt time.Time) (bool, error) {
	recorded := false
	err := st.WithTx(ctx, func(tx *sql.Tx) error {
		// Count the ids still staged: a sweep with unapplied events is not
		// complete.
		var staged int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sync_sweep_pending`).Scan(&staged); err != nil {
			return fmt.Errorf("count staged late events: %w", err)
		}
		if staged > 0 {
			return nil
		}
		recorded = true
		return upsertSweepMeta(ctx, tx, map[string]string{
			lastSweepAtKey:         startedAt.UTC().Format(time.RFC3339Nano),
			sweepAfterIDKey:        "",
			sweepAfterCreatedAtKey: "",
			sweepStartedAtKey:      "",
		})
	})
	if err != nil {
		return false, err
	}
	return recorded, nil
}

// resetLateEventSweep clears the sweep state (meta.sync.last_sweep_at, the
// listing progress and every staged id), so the next pull diffs the full
// hub history from the zero position. sync clone calls it: a clone rebuilds
// the store from the hub (MTIX-95.5).
func resetLateEventSweep(ctx context.Context, st *sqlite.Store) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		// Drop every staged id with the rest of the sweep state.
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_sweep_pending`); err != nil {
			return fmt.Errorf("clear staged late events: %w", err)
		}
		return upsertSweepMeta(ctx, tx, map[string]string{
			lastSweepAtKey:         "",
			sweepAfterIDKey:        "",
			sweepAfterCreatedAtKey: "",
			sweepStartedAtKey:      "",
		})
	})
}

// upsertSweepMeta upserts the given meta values in the caller's
// transaction, so a store whose key rows are missing records them again.
func upsertSweepMeta(ctx context.Context, tx *sql.Tx, values map[string]string) error {
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
}
