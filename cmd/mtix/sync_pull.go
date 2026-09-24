// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
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
// MTIX-15.7.2. Pulls events from the hub starting at
// meta.sync.last_pulled_clock, applies them locally via
// IdempotentApply, and advances the cursor sentinel. It then runs the
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
		Long: `Pull events from the BYO Postgres sync hub starting at the local
last_pulled_clock cursor; apply each event via the FR-18.9 idempotent
apply engine; advance the cursor.

Then sweep for late events: list the hub events created since the
previous sweep (hub time, minus a 15-minute overlap), fetch the ones
this store does not hold, and apply them the same way. This catches
events a teammate pushed after working offline, whose Lamport clock is
below the cursor. The first sweep on a store compares the full hub
event history once and prints how many late events it recovered. If
the cursor pass stops on an edit of a node whose create it has not
received, pull runs the sweep and then retries the cursor pass once.

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

// runSyncPull executes the pull flow (pullThenSweep): the Lamport-cursor
// loop, then the late-event sweep for events stamped below the cursor
// (MTIX-95.5; ADR-006 D5), whose window and recorded time come only from
// the hub's clock. A cursor pass stopped by an event whose node is missing
// runs the sweep and retries once.
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

	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := transport.New(connectCtx, dsn, opts)
	if err != nil {
		noteSyncResult(ctx, app.store, false)
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	since, err := readLastPulledClock(ctx, app.store)
	if err != nil {
		return wrapSyncErr(stderr, "read cursor", err)
	}

	// A pull into an EMPTY journal is a bootstrap: the events it brings in are
	// history, not fresh work. Detect it before the loop so the hook scan
	// floor can be initialized at the tail afterwards (FR-20 §8 — hooks never
	// fire a backlog storm on a store's first fill).
	preTail, tailErr := app.store.JournalTail(ctx)

	// The cursor loop never returns an event stamped below the cursor, such
	// as one pushed late by an offline teammate (ADR-006 D5): after it, sweep
	// for them.
	res, err := pullThenSweep(ctx, stderr, pool, app.store, since, limit)
	if err != nil {
		noteSyncResult(ctx, app.store, false)
		return wrapSyncErr(stderr, res.stage, err)
	}
	noteSyncResult(ctx, app.store, true)

	if tailErr == nil && preTail == 0 && res.pulled > 0 {
		if err := app.store.InitHookScanFloorAtTail(ctx); err != nil {
			fmt.Fprintf(stderr, "mtix sync pull: hook floor init: %s\n", err)
		}
	}

	fmt.Fprintf(stdout,
		"pull complete: %d events applied across %d batches\n", res.pulled, res.batches)
	printLateEventSweep(stdout, res.sweep)
	return nil
}

// cursorPuller is the hub surface of the cursor pass; *transport.Pool
// implements it.
type cursorPuller interface {
	PullEvents(ctx context.Context, sinceLamport int64, limit int) ([]*model.SyncEvent, bool, error)
}

// pullSweepHub is the hub surface of a whole pull: the cursor pass and the
// late-event sweep.
type pullSweepHub interface {
	cursorPuller
	lateEventHub
}

// pullOutcome is the result of pullThenSweep. stage names the step that
// returned the error, for the "mtix sync <stage>: ..." message.
type pullOutcome struct {
	pulled, batches int
	sweep           lateEventSweep
	stage           string
}

// pullThenSweep runs the cursor pass and then the late-event sweep
// (MTIX-95.5). A late push can straddle the cursor: a node's create stamped
// below it and an edit of that node above it, so the cursor pass returns
// the edit without its create and the apply fails with model.ErrNotFound
// (the failed batch rolls back; the cursor has advanced only over committed
// batches). Then, and only then, it runs the sweep, which delivers the
// create in Lamport order, and retries the cursor pass ONCE from the saved
// cursor. Any other failure, a failed sweep, or a retry that fails again is
// returned as it is.
func pullThenSweep(ctx context.Context, stderr io.Writer, hub pullSweepHub,
	st *sqlite.Store, since int64, limit int,
) (pullOutcome, error) {
	out := pullOutcome{stage: "pull loop"}
	var err error
	out.pulled, out.batches, err = pullLoop(ctx, stderr, hub, st, since, limit)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return out, err
	}
	retry := err != nil
	if retry {
		fmt.Fprintf(stderr,
			"pull: a pulled event's node is missing locally (%s); running the late-event sweep, then retrying the pull once\n",
			err)
	}
	out.stage = "late-event sweep"
	if out.sweep, err = sweepLateEvents(ctx, stderr, hub, st, limit); err != nil || !retry {
		return out, err
	}
	out.stage = "read cursor"
	if since, err = readLastPulledClock(ctx, st); err != nil {
		return out, err
	}
	out.stage = "pull loop"
	pulled, batches, err := pullLoop(ctx, stderr, hub, st, since, limit)
	out.pulled += pulled
	out.batches += batches
	return out, err
}

// pullLoop drives the pull-and-apply iteration. Mirrors cloneLoop
// from MTIX-15.7.1 but reads/writes the last_pulled_clock sentinel
// (not the clone checkpoint).
func pullLoop(ctx context.Context, stderr io.Writer,
	pool cursorPuller, store *sqlite.Store, since int64, limit int,
) (int, int, error) {
	totalPulled := 0
	batches := 0
	for {
		events, hasMore, err := pool.PullEvents(ctx, since, limit)
		if err != nil {
			return totalPulled, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			break
		}
		if err := applyPullBatch(ctx, store, events); err != nil {
			return totalPulled, batches, fmt.Errorf("apply batch %d: %w", batches+1, err)
		}
		for _, e := range events {
			if e.LamportClock > since {
				since = e.LamportClock
			}
		}
		if err := writeLastPulledClock(ctx, store, since); err != nil {
			return totalPulled, batches, fmt.Errorf("cursor write: %w", err)
		}
		totalPulled += len(events)
		batches++
		fmt.Fprintf(stderr, "pull progress: batch %d (%d events; cursor=%d)\n",
			batches, len(events), since)
		if !hasMore {
			break
		}
	}
	return totalPulled, batches, nil
}

// applyPullBatch wraps IdempotentApply for a batch in a single tx.
// Identical to clone's applyBatch but kept separate so future
// divergence (e.g. progress-reporting per event) doesn't require
// touching clone code.
//
// afterEach, when given, runs in the same transaction right after each
// event is applied: the late-event sweep (MTIX-95.5) uses it to remove the
// event's id from its staging table atomically with the apply.
func applyPullBatch(ctx context.Context, store *sqlite.Store, events []*model.SyncEvent,
	afterEach ...func(tx *sql.Tx, e *model.SyncEvent) error,
) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return fmt.Errorf("apply %s: %w", e.EventID, err)
			}
			for _, after := range afterEach {
				if err := after(tx, e); err != nil {
					return fmt.Errorf("after apply %s: %w", e.EventID, err)
				}
			}
		}
		return nil
	})
}

// readLastPulledClock returns meta.sync.last_pulled_clock or 0 when
// the row is missing (fresh DB).
func readLastPulledClock(ctx context.Context, store *sqlite.Store) (int64, error) {
	var raw string
	err := store.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.last_pulled_clock'`,
	).Scan(&raw)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cursor %q: %w", raw, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("cursor %q negative; corrupted state", raw)
	}
	return v, nil
}

// writeLastPulledClock advances the cursor sentinel after a successful
// batch apply.
func writeLastPulledClock(ctx context.Context, store *sqlite.Store, cursor int64) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.last_pulled_clock'`,
			strconv.FormatInt(cursor, 10),
		)
		return err
	})
}
