// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newSyncCloneCmd creates the `mtix sync clone` command per FR-18 /
// MTIX-15.7.1. A joiner runs this to populate a fresh local SQLite
// from the hub's full event log.
//
// Usage:
//
//	mtix sync clone <DSN>            # full clone, refuses non-empty local
//	mtix sync clone <DSN> --resume   # pick up from .mtix data checkpoint
//
// Behavior:
//  1. Resolve DSN (positional > env > .mtix/secrets).
//  2. Refuse if the local sync_events table has any rows AND --resume
//     is not set. This protects against accidentally clobbering local
//     work; the user must explicitly choose 'mtix sync reconcile
//     --discard-local' first.
//  3. Pull events in batches (PullEvents limit=batchSize); apply each
//     batch via IdempotentApply. Update meta.sync.last_pulled_clock
//     and meta.sync.clone.checkpoint after each batch so --resume can
//     pick up.
//  4. Print a progress line every batch on stderr.
//
// --resume is opt-in to avoid the surprising case where a user runs
// 'mtix sync clone' twice and gets unexpected merge behavior. The
// failure mode is loud + recoverable.
func newSyncCloneCmd() *cobra.Command {
	var (
		insecureTLS bool
		resume      bool
		batchSize   int
	)

	cmd := &cobra.Command{
		Use:   "clone [DSN]",
		Short: "Clone the sync hub into a fresh local store (FR-18)",
		Long: `Clone all events from the BYO Postgres sync hub into the local SQLite.
Refuses if the local store already has events unless --resume is set.

Use --resume to pick up an interrupted clone from the last batch
checkpoint (.mtix data sentinel meta.sync.clone.checkpoint).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncClone(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, resume, batchSize)
		},
	}

	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	cmd.Flags().BoolVar(&resume, "resume", false,
		"Resume an interrupted clone from the last checkpoint")
	cmd.Flags().IntVar(&batchSize, "batch-size", 1000,
		"Number of events to pull per batch (FR-18.20)")
	return cmd
}

// runSyncClone executes the clone flow. Extracted from the cobra
// closure for direct testability.
func runSyncClone(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, resume bool, batchSize int,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync clone: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync clone: local store not initialized")
	}
	if batchSize <= 0 {
		batchSize = 1000
	}

	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}

	if !resume {
		if hasEvents, hErr := localHasEvents(ctx, app.store); hErr != nil {
			return wrapSyncErr(stderr, "local probe", hErr)
		} else if hasEvents {
			return fmt.Errorf(
				"mtix sync clone: local sync_events not empty; " +
					"either run 'mtix sync clone --resume' or " +
					"'mtix sync reconcile --discard-local' first")
		}
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := transport.New(connectCtx, dsn, opts)
	if err != nil {
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	since, err := readCloneCheckpoint(ctx, app.store, resume)
	if err != nil {
		return wrapSyncErr(stderr, "checkpoint", err)
	}

	pulled, batches, err := cloneLoop(ctx, stderr, pool, app.store, since, batchSize)
	if err != nil {
		return wrapSyncErr(stderr, "clone loop", err)
	}

	fmt.Fprintf(stdout,
		"clone complete: %d events applied across %d batches\n", pulled, batches)
	return nil
}

// cloneLoop drives the pull-and-apply iteration. Returns the total
// number of events applied and batches consumed. Updates the
// checkpoint sentinel after each batch so --resume can pick up.
func cloneLoop(ctx context.Context, stderr io.Writer,
	pool *transport.Pool, store *sqlite.Store, since int64, batchSize int,
) (int, int, error) {
	totalPulled := 0
	batches := 0
	for {
		events, hasMore, err := pool.PullEvents(ctx, since, batchSize)
		if err != nil {
			return totalPulled, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			break
		}
		if err := applyBatch(ctx, store, events); err != nil {
			return totalPulled, batches, fmt.Errorf("apply batch %d: %w", batches+1, err)
		}
		// Advance the since cursor to the highest lamport in the batch.
		for _, e := range events {
			if e.LamportClock > since {
				since = e.LamportClock
			}
		}
		if err := writeCloneCheckpoint(ctx, store, since); err != nil {
			return totalPulled, batches, fmt.Errorf("checkpoint write: %w", err)
		}
		totalPulled += len(events)
		batches++
		fmt.Fprintf(stderr, "clone progress: batch %d (%d events; cursor=%d)\n",
			batches, len(events), since)
		if !hasMore {
			break
		}
	}
	return totalPulled, batches, nil
}

// applyBatch wraps IdempotentApply for every event in the batch
// inside a single tx for performance. A failure on any event rolls
// back the entire batch — the caller's --resume picks up from the
// last successful checkpoint.
func applyBatch(ctx context.Context, store *sqlite.Store, events []*model.SyncEvent) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return fmt.Errorf("apply %s: %w", e.EventID, err)
			}
		}
		return nil
	})
}

// localHasEvents returns true if sync_events has at least one row.
func localHasEvents(ctx context.Context, store *sqlite.Store) (bool, error) {
	var n int
	err := store.WithTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_events`).Scan(&n)
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// readCloneCheckpoint returns the last-pulled lamport. When --resume
// is set, reads from meta.sync.clone.checkpoint; otherwise returns 0.
func readCloneCheckpoint(ctx context.Context, store *sqlite.Store, resume bool) (int64, error) {
	if !resume {
		return 0, nil
	}
	var raw string
	err := store.WithTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT value FROM meta WHERE key = 'meta.sync.clone.checkpoint'`,
		).Scan(&raw)
	})
	if err != nil {
		return 0, err
	}
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0, fmt.Errorf("parse checkpoint %q: %w", raw, err)
	}
	return v, nil
}

// writeCloneCheckpoint persists the current cursor so --resume can
// pick up from this point after an interruption.
func writeCloneCheckpoint(ctx context.Context, store *sqlite.Store, cursor int64) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.clone.checkpoint'`,
			fmt.Sprintf("%d", cursor),
		)
		return err
	})
}
