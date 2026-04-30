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
// MTIX-15.7.2. Pulls events from the hub starting at
// meta.sync.last_pulled_clock, applies them locally via
// IdempotentApply, and advances the cursor sentinel.
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
		"Number of events to pull per batch")
	return cmd
}

// runSyncPull executes the pull flow.
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
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	since, err := readLastPulledClock(ctx, app.store)
	if err != nil {
		return wrapSyncErr(stderr, "read cursor", err)
	}

	pulled, batches, err := pullLoop(ctx, stderr, pool, app.store, since, limit)
	if err != nil {
		return wrapSyncErr(stderr, "pull loop", err)
	}

	fmt.Fprintf(stdout,
		"pull complete: %d events applied across %d batches\n", pulled, batches)
	return nil
}

// pullLoop drives the pull-and-apply iteration. Mirrors cloneLoop
// from MTIX-15.7.1 but reads/writes the last_pulled_clock sentinel
// (not the clone checkpoint).
func pullLoop(ctx context.Context, stderr io.Writer,
	pool *transport.Pool, store *sqlite.Store, since int64, limit int,
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
func applyPullBatch(ctx context.Context, store *sqlite.Store, events []*model.SyncEvent) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return fmt.Errorf("apply %s: %w", e.EventID, err)
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
