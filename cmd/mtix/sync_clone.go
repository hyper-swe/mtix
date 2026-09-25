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

Before it writes anything, clone runs on every hub event the checks that
'mtix sync pull' runs (the Lamport clock: below 2^53 and at most
sync.max_lamport_jump above the local clock; the FR-18.7 envelope caps;
a hub row that decodes), and refuses the whole clone when any event
fails, naming the event and the reason. Clone has no quarantine: run
'mtix sync reconcile --discard-local --yes', then 'mtix sync pull', which
quarantines such an event and applies the rest. Clone therefore reads
the hub's event log twice, once to check it and once to apply it.

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
	// Check every hub event before writing anything (MTIX-95.11), then clone.
	pulled, batches, stage, err := checkThenClone(ctx, stderr, pool, since, batchSize)
	if err != nil {
		return wrapSyncErr(stderr, stage, err)
	}

	// A clone bootstraps a store with HISTORY: initialize the hook scan floor
	// at the journal tail so hooks never treat cloned history as a backlog of
	// fresh events to fire on (FR-20 §8 — no wake storm on a new machine).
	if err := app.store.InitHookScanFloorAtTail(ctx); err != nil {
		fmt.Fprintf(stderr, "mtix sync clone: hook floor init: %s\n", err)
	}

	fmt.Fprintf(stdout,
		"clone complete: %d events applied across %d batches\n", pulled, batches)
	return nil
}

// cloneLoop drives the pull-and-apply iteration. Returns the total
// number of events applied and batches consumed. Updates the
// checkpoint sentinel after each batch so --resume can pick up.
func cloneLoop(ctx context.Context, stderr io.Writer,
	pool cursorPuller, store *sqlite.Store, since int64, batchSize int,
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
		// The check before the clone already warned about its events.
		if err := applyBatch(ctx, newPullIngest(io.Discard), store, events); err != nil {
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
// last successful checkpoint. Each event first gets the pull's checks
// against the local clock (checkPulledEvent, MTIX-95.11): the clone checked
// every event before it began (preflightClone), so this refuses only an
// event pushed to the hub since, with an error wrapping errCloneRefused.
func applyBatch(ctx context.Context, in pullIngest, store *sqlite.Store, events []*model.SyncEvent) error {
	if err := requireEvents(events); err != nil {
		return err
	}
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			local, err := sqlite.LocalLamport(ctx, tx)
			if err != nil {
				return fmt.Errorf("check %s: %w", e.EventID, err)
			}
			if refused := checkPulledEvent(in, e, local); refused != nil {
				return cloneRefusal(e, refused, true)
			}
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return fmt.Errorf("apply %s: %w", e.EventID, err)
			}
		}
		return nil
	})
}

// localHasEvents returns true if sync_events has at least one row.
//
// We only check sync_events (not nodes / dependencies / applied_events)
// because sync_events is the canonical replication log for this project:
// every mutation that lands locally writes a sync_events row in the
// same tx (FR-18.3). If sync_events is empty, the local store has no
// project-shaped state from this CLI's perspective; clone is safe.
//
// Uses readDB directly (NOT store.WithTx) per the MTIX-15.7.1 audit:
// WithTx opens a write tx, which would unnecessarily lock the DB for
// a read-only check.
func localHasEvents(ctx context.Context, store *sqlite.Store) (bool, error) {
	var n int
	err := store.QueryRow(ctx, `SELECT COUNT(*) FROM sync_events`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// readCloneCheckpoint returns the last-pulled lamport. When --resume
// is set, reads from meta.sync.clone.checkpoint; otherwise returns 0.
//
// Validates non-negative — a corrupted negative checkpoint would
// cause PullEvents(since=-1) to return ALL events from lamport 0,
// silently re-applying the entire log. Refuse instead.
func readCloneCheckpoint(ctx context.Context, store *sqlite.Store, resume bool) (int64, error) {
	if !resume {
		return 0, nil
	}
	var raw string
	err := store.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.clone.checkpoint'`,
	).Scan(&raw)
	if err != nil {
		return 0, err
	}
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0, fmt.Errorf("parse checkpoint %q: %w", raw, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("checkpoint %q is negative; corrupted state — "+
			"either restore a backup or run 'mtix sync reconcile --discard-local'", raw)
	}
	return v, nil
}

// writeCloneCheckpoint persists the current cursor so --resume can
// pick up from this point after an interruption.
//
// Uses WithTx (write tx) because this is a write — needed for SQLite
// WAL durability. The corresponding read in readCloneCheckpoint goes
// through readDB directly (audit fix).
func writeCloneCheckpoint(ctx context.Context, store *sqlite.Store, cursor int64) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.clone.checkpoint'`,
			fmt.Sprintf("%d", cursor),
		)
		return err
	})
}
