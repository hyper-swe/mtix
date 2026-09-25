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
//  3. Pull events in batches (PullEvents limit=batchSize), paging by the
//     (lamport_clock, event_id) keyset; apply each batch via
//     IdempotentApply. Save the checkpoint (meta.sync.clone.checkpoint and
//     meta.sync.clone.checkpoint_event_id) after each batch so --resume
//     can pick up, and on completion save the pull cursor
//     (meta.sync.last_pulled_clock and meta.sync.last_pulled_event_id) at
//     the last cloned event, so the next pull fetches only newer events
//     (MTIX-95.4).
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
fails, naming the event and the reason. Clone has no quarantine: on the
fresh store, run 'mtix sync pull' instead, which quarantines such an
event and applies the rest. 'mtix sync reconcile --discard-local --yes'
deletes local tasks and unpushed changes; use it only on a store that
already holds sync state, after 'mtix sync push', a pending count of 0 in
'mtix sync status' and a human's go-ahead. Clone reads the hub's event
log twice, once to check it and once to apply it.

Use --resume to pick up an interrupted clone from the last batch
checkpoint (.mtix data sentinels meta.sync.clone.checkpoint and
meta.sync.clone.checkpoint_event_id). When it completes, clone sets the
pull cursor, so the next 'mtix sync pull' fetches only newer events.`,
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

// cloneLoop drives the pull-and-apply iteration from the keyset position
// after and returns the number of events applied and batches consumed. It
// pages by (lamport_clock, event_id) like pullLoop, each page after the
// previous page's last event, so events that share a Lamport clock across a
// page boundary are all cloned (MTIX-95.4; ADR-006 D6). It saves the
// checkpoint, both halves, after each batch so --resume can pick up, and
// on completion saves the pull cursor at the same position (ADR-006 D16):
// the first pull after a clone then asks the hub only for newer events. A
// page that does not advance past the cursor stops the clone, before it is
// applied, with an error naming the cursor (pageCursor.advance).
func cloneLoop(ctx context.Context, stderr io.Writer,
	pool cursorPuller, store *sqlite.Store, after transport.PullCursor, batchSize int,
) (int, int, error) {
	totalPulled := 0
	batches := 0
	page := newPageCursor(after)
	for {
		events, hasMore, err := pool.PullEvents(ctx, page.at, batchSize)
		if err != nil {
			return totalPulled, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			break
		}
		// A nil event is invalid input; refuse it before the page is read.
		if err := requireEvents(events); err != nil {
			return totalPulled, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		if err := page.advance(events); err != nil {
			return totalPulled, batches, fmt.Errorf("pull batch %d: %w", batches+1, err)
		}
		// The check before the clone already warned about its events.
		if err := applyBatch(ctx, newPullIngest(io.Discard), store, events); err != nil {
			return totalPulled, batches, fmt.Errorf("apply batch %d: %w", batches+1, err)
		}
		if err := writeCloneCheckpoint(ctx, store, page.at); err != nil {
			return totalPulled, batches, fmt.Errorf("checkpoint write: %w", err)
		}
		totalPulled += len(events)
		batches++
		fmt.Fprintf(stderr, "clone progress: batch %d (%d events; through lamport %d)\n",
			batches, len(events), page.at.Lamport)
		if !hasMore {
			break
		}
	}
	if err := store.WithTx(ctx, func(tx *sql.Tx) error { return writePullCursor(ctx, tx, page.at) }); err != nil {
		return totalPulled, batches, fmt.Errorf("pull cursor write: %w", err)
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

// readCloneCheckpoint returns the keyset position an interrupted clone
// resumes after. When --resume is set it reads meta.sync.clone.checkpoint
// and meta.sync.clone.checkpoint_event_id in one statement (MTIX-95.4);
// otherwise it returns the start of the log. An event-id half that is
// absent or empty (a checkpoint saved before MTIX-95.4) reads as "", so
// the resume reads the events at exactly the saved clock again and
// applies the ones it holds idempotently.
//
// Validates non-negative — a corrupted negative checkpoint would
// cause PullEvents(since=-1) to return ALL events from lamport 0,
// silently re-applying the entire log. Refuse instead.
func readCloneCheckpoint(ctx context.Context, store *sqlite.Store, resume bool) (transport.PullCursor, error) {
	if !resume {
		return transport.PullCursor{}, nil
	}
	var raw sql.NullString
	var eventID string
	// Both checkpoint keys in one read.
	err := store.QueryRow(ctx, `
		SELECT (SELECT value FROM meta WHERE key = 'meta.sync.clone.checkpoint'),
		       COALESCE((SELECT value FROM meta WHERE key = 'meta.sync.clone.checkpoint_event_id'), '')`,
	).Scan(&raw, &eventID)
	if err != nil {
		return transport.PullCursor{}, fmt.Errorf("read clone checkpoint: %w", err)
	}
	if !raw.Valid {
		return transport.PullCursor{}, fmt.Errorf("read clone checkpoint: meta.sync.clone.checkpoint: %w", sql.ErrNoRows)
	}
	var v int64
	if _, err := fmt.Sscanf(raw.String, "%d", &v); err != nil {
		return transport.PullCursor{}, fmt.Errorf("parse checkpoint %q: %w", raw.String, err)
	}
	if v < 0 {
		return transport.PullCursor{}, fmt.Errorf("checkpoint %q is negative; corrupted state — "+
			"either restore a backup or run 'mtix sync reconcile --discard-local'", raw.String)
	}
	return transport.PullCursor{Lamport: v, EventID: eventID}, nil
}

// writeCloneCheckpoint persists the clone's keyset position, both halves,
// so --resume can pick up after this point after an interruption
// (MTIX-95.4).
//
// Uses WithTx (write tx) because this is a write — needed for SQLite
// WAL durability. The corresponding read in readCloneCheckpoint goes
// through readDB directly (audit fix).
func writeCloneCheckpoint(ctx context.Context, store *sqlite.Store, cursor transport.PullCursor) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		return upsertMeta(ctx, tx,
			[2]string{"meta.sync.clone.checkpoint", strconv.FormatInt(cursor.Lamport, 10)},
			[2]string{"meta.sync.clone.checkpoint_event_id", cursor.EventID})
	})
}
