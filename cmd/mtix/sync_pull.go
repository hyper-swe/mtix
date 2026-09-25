// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pullDefaultBatchSize is the default --limit value. Matches the
// clone batch size from MTIX-15.7.1 for symmetry.
const pullDefaultBatchSize = 1000

// newSyncPullCmd creates the `mtix sync pull` command per FR-18 /
// MTIX-15.7.2. Pulls events from the hub after the pull cursor
// (meta.sync.last_pulled_clock and meta.sync.last_pulled_event_id,
// MTIX-95.4), applies them locally via IdempotentApply, and advances the
// cursor in each batch's transaction. It then runs the
// MTIX-95.5 late-event sweep (sync_pull_sweep.go) for events stamped
// below the cursor.
//
// Unlike clone, pull does NOT refuse a non-empty local store — it's
// the routine command for ongoing sync. Pull is also lock-free
// (multiple processes pulling concurrently is harmless: applied_events
// dedupes on event_id).
func newSyncPullCmd() *cobra.Command {
	var (
		insecureTLS bool
		limit       int
	)
	cmd := &cobra.Command{
		Use:   "pull [DSN]",
		Short: "Pull events from the sync hub and apply locally (FR-18)",
		Long: `Pull events from the BYO Postgres sync hub, starting after the local
pull cursor (the Lamport clock and event id of the last event pulled);
apply each event via the FR-18.9 idempotent apply engine; save the
cursor in the same transaction as each batch.

Then sweep for late events: list the hub events created since the
previous sweep (hub time, minus a 15-minute overlap), fetch the ones
this store does not hold, and apply them the same way. This catches
events a teammate pushed after working offline, whose Lamport clock is
below the cursor. The first sweep on a store compares the full hub
event history once and prints how many late events it recovered.

Every pulled event is checked before it is applied: its Lamport clock
(below 2^53 and at most sync.max_lamport_jump above the local clock;
the config key defaults to 4294967296, that is 2^32), then the FR-18.7
envelope caps (payload size and depth, vector-clock limits, id
grammars). An event stamped more than 24h ahead of this machine's clock
is applied with a warning. Each event applies in its own savepoint: one
that fails a check or its apply is rolled back, kept in the local
quarantine (table sync_quarantine), and the pull goes on past it; the
cursor never moves to a refused clock. Every pull retries the
quarantine first, before contacting the hub, and again after it
applied events, so an event whose node or dependency target arrives
later applies then. 'mtix sync quarantine list' shows quarantined
events, 'mtix sync status' counts them, and 'mtix sync doctor' fails
while any remain.

Lock-free: multiple processes pulling concurrently is safe because
applied_events dedupes on event_id.

Hook mode (MTIX_SYNC_HOOK=1) warn-and-skips on transient PG errors.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPull(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, limit)
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	cmd.Flags().IntVar(&limit, "limit", pullDefaultBatchSize,
		"Number of events to pull per batch (also the late-event sweep page size)")
	return cmd
}

// runSyncPull executes the pull flow: it retries the quarantine of pulled
// events (MTIX-95.11) before contacting the hub, then runs pullThenSweep:
// the Lamport-cursor loop, the late-event sweep for events stamped below
// the cursor (MTIX-95.5; ADR-006 D5), whose window and recorded time come
// only from the hub's clock, and the quarantine retry after a pull that
// applied events.
func runSyncPull(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, limit int,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync pull: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync pull: local store not initialized")
	}
	if limit <= 0 {
		limit = pullDefaultBatchSize
	}
	in := newPullIngest(stderr)

	// A pull into an EMPTY journal is a bootstrap: the events it brings in are
	// history, not fresh work. Detect it before anything is applied (the
	// quarantine retry below applies events too) so the hook scan floor can
	// be initialized at the tail afterwards (FR-20 §8 — hooks never fire a
	// backlog storm on a store's first fill).
	preTail, tailErr := app.store.JournalTail(ctx)
	bootstrap := tailErr == nil && preTail == 0

	// Quarantined events are retried at the start of every pull, before the
	// hub is contacted, so one whose missing node or dependency target has
	// arrived since applies even when the hub is unreachable (MTIX-95.11).
	first, err := retryQuarantinedEvents(ctx, in, app.store, limit)
	if err != nil {
		return wrapSyncErr(stderr, "quarantine retry", err)
	}
	// The retry's events are part of the first fill too; set the floor now,
	// before a hub failure below can return.
	initBootstrapHookFloor(ctx, stderr, bootstrap && first.applied > 0)

	res, err := connectAndPull(ctx, in, args, opts, limit)
	if err != nil {
		return wrapSyncErr(stderr, res.stage, err)
	}
	initBootstrapHookFloor(ctx, stderr, bootstrap && res.pulled+res.retried > 0)

	fmt.Fprintf(stdout,
		"pull complete: %d events applied across %d batches\n", res.pulled, res.batches)
	printLateEventSweep(stdout, res.sweep)
	printQuarantineHeld(ctx, stdout, stderr, app.store)
	return nil
}

// initBootstrapHookFloor moves the hook scan floor to the journal tail when
// init is true: the events a first fill of an empty journal applied are
// history, so hooks never fire on them as a backlog (FR-20 §8).
func initBootstrapHookFloor(ctx context.Context, stderr io.Writer, init bool) {
	if !init {
		return
	}
	if err := app.store.InitHookScanFloorAtTail(ctx); err != nil {
		fmt.Fprintf(stderr, "mtix sync pull: hook floor init: %s\n", err)
	}
}

// connectAndPull resolves the DSN, connects to the hub and runs
// pullThenSweep from the saved cursor, naming the failed stage in the
// outcome (dsn, connect, read cursor or a pullThenSweep stage). A failed
// connect or pull counts one sync error (meta.sync.consecutive_errors) and
// a completed pull clears the count; a DSN or cursor failure leaves it.
func connectAndPull(ctx context.Context, in pullIngest, args []string,
	opts transport.Options, limit int,
) (pullOutcome, error) {
	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return pullOutcome{stage: "dsn"}, err
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := transport.New(connectCtx, dsn, opts)
	if err != nil {
		noteSyncResult(ctx, app.store, false)
		return pullOutcome{stage: "connect"}, err
	}
	defer pool.Close()

	since, err := readLastPulledClock(ctx, app.store)
	if err != nil {
		return pullOutcome{stage: "read cursor"}, err
	}

	// The cursor loop never returns an event stamped below the cursor, such
	// as one pushed late by an offline teammate (ADR-006 D5): after it, sweep
	// for them.
	res, err := pullThenSweep(ctx, in, pool, app.store, since, limit)
	noteSyncResult(ctx, app.store, err == nil)
	return res, err
}

// cursorPuller is the hub surface of the cursor pass; *transport.Pool
// implements it.
type cursorPuller interface {
	PullEvents(ctx context.Context, after transport.PullCursor, limit int) ([]*model.SyncEvent, bool, error)
}

// pullSweepHub is the hub surface of a whole pull: the cursor pass and the
// late-event sweep.
type pullSweepHub interface {
	cursorPuller
	lateEventHub
}

// pullOutcome is the result of pullThenSweep. stage names the step that
// returned the error, for the "mtix sync <stage>: ..." message. pulled
// counts the events the cursor pass applied (not the ones it
// quarantined), and retried the quarantined events the end-of-pull retry
// applied.
type pullOutcome struct {
	pulled, batches int
	sweep           lateEventSweep
	stage           string
	retried         int
}

// pullThenSweep runs the cursor pass, then the late-event sweep
// (MTIX-95.5), then, when either applied an event, a retry of the
// quarantine (MTIX-95.11): an event this pull brought can be what a
// quarantined event lacked, such as a link_dep's target, or the create of
// a node whose edit the cursor pass met first (a late push can straddle
// the cursor, with the create stamped below it and the edit above; the
// sweep delivers the create). A pulled event that fails never fails the
// pull: it is quarantined. Any other failure is returned with its stage.
func pullThenSweep(ctx context.Context, in pullIngest, hub pullSweepHub,
	st *sqlite.Store, since transport.PullCursor, limit int,
) (pullOutcome, error) {
	out := pullOutcome{stage: "pull loop"}
	var err error
	if out.pulled, out.batches, err = pullLoop(ctx, in, hub, st, since, limit); err != nil {
		return out, err
	}
	out.stage = "late-event sweep"
	if out.sweep, err = sweepLateEvents(ctx, in, hub, st, limit); err != nil {
		return out, err
	}
	if out.pulled+out.sweep.Recovered == 0 {
		return out, nil
	}
	retry, err := retryQuarantinedEvents(ctx, in, st, limit)
	out.retried = retry.applied
	if err != nil {
		out.stage = "quarantine retry"
	}
	return out, err
}

// pullLoop drives the pull-and-apply iteration from the keyset position
// after (MTIX-95.4). Mirrors cloneLoop from MTIX-15.7.1. Each page is asked
// for after the previous page's last event, its Lamport clock and its event
// id, so events that share a clock across a page boundary are all pulled
// (ADR-006 D6). Each batch is applied event by event (applyPullBatch): an
// event that fails is quarantined and the batch continues past it
// (MTIX-95.11). applyPullBatch saves the pull cursor in the batch's own
// transaction, never at a refused Lamport clock (advancePullCursor); the
// next page still starts after the whole batch, so this pull does not
// fetch a refused event again. A page that does not advance past the
// cursor stops the loop, before it is applied, with an error naming the
// cursor (pageCursor.advance), instead of being asked for forever. It
// returns how many events applied and how many batches it read.
func pullLoop(ctx context.Context, in pullIngest,
	pool cursorPuller, store *sqlite.Store, after transport.PullCursor, limit int,
) (int, int, error) {
	applied, batches := 0, 0
	page := newPageCursor(after)
	for {
		events, hasMore, err := pool.PullEvents(ctx, page.at, limit)
		if err != nil {
			return applied, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			break
		}
		// A nil event is invalid input; refuse it before the page is read.
		if err = requireEvents(events); err != nil {
			return applied, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		if err = page.advance(events); err != nil {
			return applied, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		held, err := applyPullBatch(ctx, in, store, quarantineSourcePull, events)
		if err != nil {
			return applied, batches, fmt.Errorf("apply batch %d: %w", batches+1, err)
		}
		applied += len(events) - len(held)
		batches++
		fmt.Fprintf(in.stderr, "pull progress: batch %d (%d events, %d quarantined; through lamport %d)\n",
			batches, len(events), len(held), page.at.Lamport)
		if !hasMore {
			break
		}
	}
	return applied, batches, nil
}

// applyPullBatch applies a batch of pulled events in one transaction, each
// in its own savepoint (MTIX-95.11). Clone keeps its own all-or-nothing
// applyBatch.
//
// Each event goes through ingestPulledEvent. It must first pass
// admitPulledEvent: its Lamport clock (below 2^53 and at most
// sync.max_lamport_jump above the local clock), then the FR-18.7 envelope
// validation with its caps (payload size and depth, vector-clock limits,
// id grammars; the 24h clock-relative check only warns). It is then
// applied through IdempotentApply in its own savepoint (applyPulledEvent).
// An event that fails a check or its apply has its writes rolled back and
// is stored in sync_quarantine, tagged with source, instead of failing the
// batch, and the batch goes on with the next event. An event already
// quarantined is left to the quarantine retry, its row untouched. It
// returns the events it held, with the reasons. Only a failure of the
// transaction itself (reading the local clock, a savepoint or quarantine
// write, afterEach, an ended context) fails the batch, which then rolls
// back as a whole.
//
// afterEach, when given, runs in the same transaction right after each
// event is applied or quarantined: the late-event sweep (MTIX-95.5) uses it
// to remove the event's id from its staging table atomically with the
// apply or the quarantine.
//
// A batch of the cursor pass (source quarantineSourcePull) also saves the
// pull cursor, both halves, in the same transaction, after its last event
// (savePullCursor, MTIX-95.4; ADR-006 D6): the batch's applies, its
// quarantine rows and the cursor commit together or not at all, so a crash
// between the apply and the cursor write cannot happen. A sweep batch never
// moves the cursor.
func applyPullBatch(ctx context.Context, in pullIngest, store *sqlite.Store, source string,
	events []*model.SyncEvent, afterEach ...func(tx *sql.Tx, e *model.SyncEvent) error,
) (heldEvents, error) {
	if err := requireEvents(events); err != nil {
		return nil, err
	}
	var held heldEvents
	err := store.WithTx(ctx, func(tx *sql.Tx) error {
		held = heldEvents{}
		for _, e := range events {
			// Leave an already quarantined event to the retry; else check its
			// clock and envelope (admitPulledEvent), apply it in a savepoint
			// (applyPulledEvent), and quarantine it if either fails.
			refused, err := ingestPulledEvent(ctx, tx, in, source, e)
			if err == nil {
				err = runAfterEach(tx, e, afterEach)
			}
			if err != nil {
				return err
			}
			if refused != nil {
				held[e.EventID] = refused
			}
		}
		if source != quarantineSourcePull {
			return nil
		}
		return savePullCursor(ctx, tx, in, events)
	})
	if err != nil {
		return nil, err
	}
	reportQuarantined(in.stderr, source, events, held)
	return held, nil
}

// requireEvents refuses a batch that holds a nil event: it has no id, so
// it could be neither applied nor quarantined, and skipping it would lose
// it silently.
func requireEvents(events []*model.SyncEvent) error {
	for i, e := range events {
		if e == nil {
			return fmt.Errorf("pulled batch: event %d is nil: %w", i, model.ErrInvalidInput)
		}
	}
	return nil
}

// runAfterEach runs applyPullBatch's afterEach callbacks for e, in its
// transaction.
func runAfterEach(tx *sql.Tx, e *model.SyncEvent,
	afterEach []func(tx *sql.Tx, e *model.SyncEvent) error,
) error {
	for _, after := range afterEach {
		if err := after(tx, e); err != nil {
			return fmt.Errorf("after apply %s: %w", e.EventID, err)
		}
	}
	return nil
}

// savePullCursor saves, in the batch transaction tx, the pull cursor after
// a cursor-pass batch (MTIX-95.4): at the last event whose Lamport clock
// advancePullCursor accepts against the local clock after the batch. A
// batch with no such event leaves the saved cursor where it was.
func savePullCursor(ctx context.Context, tx *sql.Tx, in pullIngest, events []*model.SyncEvent) error {
	local, err := sqlite.LocalLamport(ctx, tx)
	if err != nil {
		return fmt.Errorf("pull cursor: %w", err)
	}
	cursor, ok := advancePullCursor(events, local, in.maxJump)
	if !ok {
		return nil
	}
	return writePullCursor(ctx, tx, cursor)
}

// readLastPulledClock returns the saved pull cursor (MTIX-95.4; ADR-006
// D6): meta.sync.last_pulled_clock and meta.sync.last_pulled_event_id,
// read in one statement. The Lamport half must exist and be a non-negative
// integer. An event-id half that is absent or empty reads as "": a store
// upgraded from a Lamport-only cursor (the key is seeded empty when this
// version opens it). An empty event id sorts before every event id, so the
// next cursor pass reads the events at exactly the saved clock again and
// applied_events dedupes the ones already applied, while any the old
// Lamport-only paging skipped at that clock are applied.
func readLastPulledClock(ctx context.Context, store *sqlite.Store) (transport.PullCursor, error) {
	var raw sql.NullString
	var eventID string
	// Both cursor keys in one read, so a concurrent pull's write is seen whole.
	err := store.QueryRow(ctx, `
		SELECT (SELECT value FROM meta WHERE key = 'meta.sync.last_pulled_clock'),
		       COALESCE((SELECT value FROM meta WHERE key = 'meta.sync.last_pulled_event_id'), '')`,
	).Scan(&raw, &eventID)
	if err != nil {
		return transport.PullCursor{}, fmt.Errorf("read pull cursor: %w", err)
	}
	if !raw.Valid {
		return transport.PullCursor{}, fmt.Errorf("read pull cursor: meta.sync.last_pulled_clock: %w", sql.ErrNoRows)
	}
	v, err := strconv.ParseInt(raw.String, 10, 64)
	if err != nil {
		return transport.PullCursor{}, fmt.Errorf("parse cursor %q: %w", raw.String, err)
	}
	if v < 0 {
		return transport.PullCursor{}, fmt.Errorf("cursor %q negative; corrupted state", raw.String)
	}
	return transport.PullCursor{Lamport: v, EventID: eventID}, nil
}

// writePullCursor saves cursor as the pull cursor, both halves, in tx
// (MTIX-95.4). Each key is upserted, so a store that lacks the event-id key
// gains it.
func writePullCursor(ctx context.Context, tx *sql.Tx, cursor transport.PullCursor) error {
	return upsertMeta(ctx, tx,
		[2]string{"meta.sync.last_pulled_clock", strconv.FormatInt(cursor.Lamport, 10)},
		[2]string{"meta.sync.last_pulled_event_id", cursor.EventID})
}

// upsertMeta sets each {key, value} pair in meta, in tx and in order, adding
// a row that is absent.
func upsertMeta(ctx context.Context, tx *sql.Tx, pairs ...[2]string) error {
	for _, kv := range pairs {
		// Parameterized upsert of one meta key.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("write %s: %w", kv[0], err)
		}
	}
	return nil
}
