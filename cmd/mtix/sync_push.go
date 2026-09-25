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

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// pushBatchSize is the per-batch event count for PushEvents per
// SYNC-DESIGN section 6.2 (10K-events-per-project envelope means a
// typical push handles tens to hundreds at most).
const pushBatchSize = 100

// newSyncPushCmd creates the `mtix sync push` command per FR-18 /
// MTIX-15.7.2. Reads pending sync_events, transmits in batches via
// transport.PushEvents, marks accepted rows as sync_status='pushed'.
//
// Singleton pusher lock per FR-18.18: acquires .mtix/data/sync.push.lock
// before pushing. If another mtix process holds the lock, exits with
// a non-error message (the ongoing push will eventually drain the
// queue). --force bypasses the lock for debugging.
func newSyncPushCmd() *cobra.Command {
	var (
		insecureTLS bool
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "push [DSN]",
		Short: "Push pending events to the sync hub (FR-18)",
		Long: `Push every event with sync_status='pending' to the BYO Postgres hub
in batches. Marks pushed events as sync_status='pushed' so re-running
the command is a no-op until new mutations land.

Acquires .mtix/data/sync.push.lock so concurrent agents on one
machine never stampede the hub. If the lock is held by another
process, exits cleanly without error (the holder will drain the
queue). Use --force to bypass the lock (debugging only).

Hook mode (MTIX_SYNC_HOOK=1) warn-and-skips on transient PG errors
so git pre-push hooks never block code pushes.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncPush(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, force)
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	cmd.Flags().BoolVar(&force, "force", false,
		"Bypass the singleton pusher lock (debugging only)")
	return cmd
}

// runSyncPush executes the push flow. Extracted from the cobra closure
// for direct testability.
func runSyncPush(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, force bool,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync push: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync push: local store not initialized")
	}

	// Singleton lock per FR-18.18. Skip when --force.
	if !force {
		lock, err := pushlock.Acquire(app.mtixDir)
		if errors.Is(err, pushlock.ErrLockHeld) {
			fmt.Fprintln(stderr,
				"mtix sync push: another process is pushing; skipping (the holder will drain the queue)")
			return nil
		}
		if err != nil {
			return wrapSyncErr(stderr, "pushlock", err)
		}
		defer func() { _ = lock.Release() }()
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

	// Report this CLI's build version into each push so the
	// version-negotiation gate (ADR-003 §7 Phase 1.5/3) sees the latest
	// version of every active client. No-op when the machine hash can't
	// be computed (treated as "no identity"; the gate stays closed).
	pool.SetClientIdentity(clientMachineHash(), version)

	return pushAndReport(ctx, stdout, stderr, pool, app.store)
}

// pushAndReport runs pushLoop against pool, records the outcome for the
// hub-unreachable counter and prints the summary: the pushed, renumbered
// and conflict counts, then how many events push holds (MTIX-95.12).
func pushAndReport(ctx context.Context, stdout, stderr io.Writer, pool eventPusher, store *sqlite.Store) error {
	pushed, batches, conflicts, renumbered, err := pushLoop(ctx, stderr, pool, store)
	if err != nil {
		noteSyncResult(ctx, store, false)
		return wrapSyncErr(stderr, "push loop", err)
	}
	noteSyncResult(ctx, store, true)

	fmt.Fprintf(stdout,
		"push complete: %d events pushed across %d batches; %d renumbered, %d conflicts surfaced\n",
		pushed, batches, renumbered, conflicts)
	printHeldPushEvents(ctx, stdout, stderr, store)
	return nil
}

// pushLoop reads pending events from the local sync_events log in
// batches and ships each batch through transport.PushEvents. After a
// successful push, accepted event_ids are marked sync_status='pushed'
// in a single tx so a crash mid-loop leaves the queue at a known
// state (still pending; the next push retries).
//
// Each event is validated before its batch is sent (MTIX-95.12, review
// F-15): an event the hub would refuse, such as one whose payload is over
// the 64 KB wire cap, is held in sync_quarantine with source push and the
// reason (heldIndex.holdBatch), and the rest of the batch is pushed. The hub
// refuses a whole batch that holds an invalid event (FR-18.7), so before
// this one such event failed every push. Held events stay pending and are
// left out of the next reads (readPendingBatch). While a task's creation is
// held, every event about that task or a task of its subtree is held with
// it. Every push first releases the holds that have ended
// (releasePushHolds): a stamp that is no longer too far ahead, and the
// valid events of the subtree of a creation released that way, which then
// go through this push as ordinary pending events.
func pushLoop(ctx context.Context, stderr io.Writer,
	pool eventPusher, store *sqlite.Store,
) (totalPushed, batches, totalConflicts, totalRenumbered int, err error) {
	idx, err := newHeldIndex(ctx, stderr, store)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("release push holds: %w", err)
	}
	var after pendingCursor // read on past each batch, held events included
	for {
		events, err := readPendingBatchFrom(ctx, store, after, pushBatchSize)
		if err != nil {
			return totalPushed, batches, totalConflicts, totalRenumbered,
				fmt.Errorf("read pending batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			return totalPushed, batches, totalConflicts, totalRenumbered, nil
		}
		after = cursorAfter(events)
		events, held, err := idx.holdBatch(ctx, stderr, store, events)
		if err != nil {
			return totalPushed, batches, totalConflicts, totalRenumbered,
				fmt.Errorf("hold invalid events of batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			if held == 0 {
				return totalPushed, batches, totalConflicts, totalRenumbered, nil
			}
			continue // every event of the batch is now held; read on past them
		}
		acceptedIDs, conflicts, renumbers, err := pushBatch(ctx, pool, store, events, batches+1)
		if err != nil {
			return totalPushed, batches, totalConflicts, totalRenumbered, err
		}
		if len(renumbers) > 0 {
			after = pendingCursor{} // re-queued creates keep their clock: read them again
			idx.stale = true        // the renumbers moved subtrees: read the held creations again
		}
		totalPushed += len(acceptedIDs)
		totalConflicts += len(conflicts)
		totalRenumbered += len(renumbers)
		batches++
		fmt.Fprintf(stderr, "push progress: batch %d (%d sent, %d accepted, %d renumbered, %d conflicts)\n",
			batches, len(events), len(acceptedIDs), len(renumbers), len(conflicts))
		// No-progress guard: a batch that neither accepted nor renumbered any
		// event (e.g. pure conflicts) makes no headway — stop so the loop can
		// never spin forever.
		if len(acceptedIDs) == 0 && len(renumbers) == 0 {
			return totalPushed, batches, totalConflicts, totalRenumbered, nil
		}
	}
}

// readPendingBatch returns up to limit pending events from sync_events in
// lamport order, leaving out the events push holds (MTIX-95.12): it is
// readPendingBatchFrom from the start of the queue.
func readPendingBatch(ctx context.Context, store *sqlite.Store, limit int) ([]*model.SyncEvent, error) {
	return readPendingBatchFrom(ctx, store, pendingCursor{}, limit)
}

// pendingCursor is a position in the pending queue: the Lamport clock and
// event id of the last event read. The zero value is the start.
type pendingCursor struct {
	lamport int64
	eventID string
}

// cursorAfter returns the position after the last of events.
func cursorAfter(events []*model.SyncEvent) pendingCursor {
	last := events[len(events)-1]
	return pendingCursor{lamport: last.LamportClock, eventID: last.EventID}
}

// readPendingBatchFrom returns up to limit pending events from sync_events
// after the cursor, in (lamport, event id) order, leaving out the events
// push holds (sync_quarantine source push, MTIX-95.12): a held event would
// otherwise stay at the head of the queue and, once a batch was full of
// them, stop every later event from being read. pushLoop reads on past
// each batch with the cursor, so held events at the head of the queue are
// skipped once per push rather than once per batch. Reads via readDB (no
// write tx needed).
func readPendingBatchFrom(ctx context.Context, store *sqlite.Store, after pendingCursor, limit int) ([]*model.SyncEvent, error) {
	// Pending events after the cursor in Lamport order, minus the held ones
	// (one primary-key probe of sync_quarantine per event). The bare
	// lamport_clock >= ? bound lets the pending index seek to the cursor
	// instead of scanning from the head of the queue.
	rows, err := store.Query(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending'
		  AND lamport_clock >= ? AND (lamport_clock > ? OR event_id > ?)
		  AND NOT EXISTS (SELECT 1 FROM sync_quarantine q
		                  WHERE q.event_id = sync_events.event_id AND q.source = 'push')
		ORDER BY lamport_clock ASC, event_id ASC
		LIMIT ?`, after.lamport, after.lamport, after.eventID, limit)
	if err != nil {
		return nil, err
	}
	return scanSyncEvents(rows, limit)
}

// scanSyncEvents reads the events of rows, which select event_id,
// project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock,
// vector_clock, author_id and author_machine_hash, and closes rows.
func scanSyncEvents(rows *sql.Rows, capHint int) ([]*model.SyncEvent, error) {
	defer func() { _ = rows.Close() }()
	out := make([]*model.SyncEvent, 0, capHint)
	for rows.Next() {
		var e model.SyncEvent
		var opType, payload, vc string
		if scanErr := rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash,
		); scanErr != nil {
			return nil, scanErr
		}
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		if err := json.Unmarshal([]byte(vc), &e.VectorClock); err != nil {
			return nil, fmt.Errorf("decode VC for %s: %w", e.EventID, err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// pushBatch sends one batch of valid events to the hub, marks the accepted
// ones pushed and drains the renumber-required outcomes (ADR-003 §6,
// MTIX-30.7): the hub rejected those creates because their number is
// already held, so the next free sibling is re-claimed, the node renumbered
// locally and the create re-queued ('pending') so a later batch carries a
// distinct number. batch numbers the batch in errors.
func pushBatch(ctx context.Context, pool eventPusher, store *sqlite.Store,
	events []*model.SyncEvent, batch int,
) ([]string, []transport.ConflictDescriptor, []transport.RenumberRequired, error) {
	acceptedIDs, conflicts, renumbers, err := pool.PushEventsWithRenumbers(ctx, events)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("push batch %d: %w", batch, err)
	}
	if err := markPushed(ctx, store, acceptedIDs); err != nil {
		return nil, nil, nil, fmt.Errorf("mark pushed batch %d: %w", batch, err)
	}
	for _, r := range renumbers {
		if _, err := store.RenumberForHubRejection(ctx, r.EventID); err != nil {
			return nil, nil, nil, fmt.Errorf("resolve renumber for %s (batch %d): %w", r.EventID, batch, err)
		}
	}
	return acceptedIDs, conflicts, renumbers, nil
}

// markPushed updates sync_status from 'pending' to 'pushed' for every
// event_id the hub accepted. Done in a single tx for atomicity; if
// the UPDATE fails, the events remain pending and the next push tries
// again (idempotent on the hub side via ON CONFLICT DO NOTHING).
func markPushed(ctx context.Context, store *sqlite.Store, acceptedIDs []string) error {
	if len(acceptedIDs) == 0 {
		return nil
	}
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, id := range acceptedIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`, id,
			); err != nil {
				return fmt.Errorf("mark %s pushed: %w", id, err)
			}
		}
		return nil
	})
}
