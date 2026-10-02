// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
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
// hub-unreachable counter and prints the summary: the pushed count, split
// into the events the hub inserted and the events it already held
// (MTIX-95.3), the renumbered and conflict counts, then how many events push
// holds (MTIX-95.12).
func pushAndReport(ctx context.Context, stdout, stderr io.Writer, pool eventPusher, store *sqlite.Store) error {
	tot, err := pushLoop(ctx, stderr, pool, store)
	if err != nil {
		noteSyncResult(ctx, store, false)
		return wrapSyncErr(stderr, "push loop", err)
	}
	noteSyncResult(ctx, store, true)

	fmt.Fprintf(stdout,
		"push complete: %d events pushed across %d batches (%d inserted, %d already on the hub); "+
			"%d renumbered, %d conflicts surfaced\n",
		tot.pushed(), tot.batches, tot.inserted, tot.present, tot.renumbered, tot.conflicts)
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
//
// An event already on the hub is acknowledged and marked pushed like an
// inserted one (MTIX-95.3, ADR-006 D7): its first push reached the hub, but
// the local mark was lost (a lost commit acknowledgement, a crash or disk
// failure before markPushed, a retry after a network drop). Before, the hub
// acknowledged nothing for such an event, so it stayed pending, and a full
// batch of them at the head of the queue stopped the push at the
// no-progress guard with everything behind it unsent. Each batch's progress
// line and the returned totals count inserted and already-present events
// apart. An event whose id the hub holds with other content is held
// instead (holdMismatches) and counts as progress, since it leaves the
// queue's reads.
func pushLoop(ctx context.Context, stderr io.Writer,
	pool eventPusher, store *sqlite.Store,
) (pushTotals, error) {
	idx, err := newHeldIndex(ctx, stderr, store)
	if err != nil {
		return pushTotals{}, fmt.Errorf("release push holds: %w", err)
	}
	run := &pushRun{ctx: ctx, stderr: stderr, pool: pool, store: store, idx: idx}
	for {
		done, err := run.step()
		if err != nil || done {
			return run.tot, err
		}
	}
}

// pushRun is the state of one pushLoop: the hub and store it works on, the
// held creations, what it has pushed, the cursor it reads on from (past each
// batch, held and deferred events included) and its memory of deferred events
// (awaitRedo, MTIX-95.37).
type pushRun struct {
	ctx    context.Context
	stderr io.Writer
	pool   eventPusher
	store  *sqlite.Store
	idx    *heldIndex
	tot    pushTotals
	after  pendingCursor
	await  awaitRedo
}

// step reads, screens and sends one batch. It reports true when the push is
// over: the queue is drained, or a batch made no headway.
func (r *pushRun) step() (bool, error) {
	start := r.after
	events, err := readPendingBatchFrom(r.ctx, r.store, r.after, pushBatchSize)
	if err != nil {
		return true, fmt.Errorf("read pending batch %d: %w", r.tot.batches+1, err)
	}
	if len(events) == 0 {
		if back, ok := r.await.back(); ok {
			r.after = back // the creations the deferred events waited for are on the hub now
			return false, nil
		}
		return true, nil
	}
	r.after = cursorAfter(events)
	events, skipped, err := r.screen(start, events)
	if err != nil {
		return true, err
	}
	if len(events) == 0 {
		return skipped == 0, nil // every event of the batch is held or waits; read on past them
	}
	return r.send(events)
}

// screen holds the events of the batch that are invalid or in a held
// creation's subtree (MTIX-95.12) and defers the ones that wait for a pending
// creation (MTIX-95.37; start is where the batch was read). It returns the
// events left to send and how many it held or deferred.
func (r *pushRun) screen(start pendingCursor, events []*model.SyncEvent) ([]*model.SyncEvent, int, error) {
	events, held, err := r.idx.holdBatch(r.ctx, r.stderr, r.store, events)
	if err != nil {
		return nil, 0, fmt.Errorf("hold invalid events of batch %d: %w", r.tot.batches+1, err)
	}
	events, deferred, err := deferAwaitingCreation(r.ctx, r.store, events)
	if err != nil {
		return nil, 0, fmt.Errorf("defer events of batch %d: %w", r.tot.batches+1, err)
	}
	r.await.note(start, deferred)
	return events, held + deferred, nil
}

// send pushes one screened batch and folds the result into the totals. It
// reports true when the batch made no headway.
func (r *pushRun) send(events []*model.SyncEvent) (bool, error) {
	res, mismatched, err := pushBatch(r.ctx, r.stderr, r.pool, r.store, events, r.tot.batches+1)
	if err != nil {
		return true, err
	}
	if len(res.Renumbers) > 0 {
		r.after = pendingCursor{} // re-queued creates keep their clock: read them again
		r.await.reset()
	}
	// The renumbers moved subtrees, and a held mismatch may be a task's
	// creation, which holds its subtree: read the held creations again.
	r.idx.stale = r.idx.stale || len(res.Renumbers)+mismatched > 0
	r.tot.add(res, mismatched)
	fmt.Fprintf(r.stderr, "push progress: batch %d (%d sent, %d accepted: %d inserted, %d already on the hub; "+
		"%d renumbered, %d conflicts)\n", r.tot.batches, len(events), len(res.Inserted)+len(res.AlreadyPresent),
		len(res.Inserted), len(res.AlreadyPresent), len(res.Renumbers), len(res.Conflicts))
	// No-progress guard: a batch that neither acknowledged (inserted or
	// already on the hub, MTIX-95.3), renumbered nor held any event (e.g.
	// pure conflicts) makes no headway — stop so the loop can never spin
	// forever.
	if len(res.Inserted)+len(res.AlreadyPresent)+len(res.Renumbers)+mismatched == 0 {
		return true, nil
	}
	r.await.progressed = true
	return false, nil
}

// pushTotals counts what one push did (MTIX-95.3): the events the hub
// inserted and the events it already held, both marked pushed; the batches
// sent; the renumbers and conflicts; and the events held because the hub
// holds their id with other content.
type pushTotals struct {
	inserted, present, batches, conflicts, renumbered, mismatched int
}

// pushed returns how many events the push marked pushed.
func (t pushTotals) pushed() int {
	return t.inserted + t.present
}

// add counts one pushed batch: its result res and the mismatched events
// it held.
func (t *pushTotals) add(res transport.PushResult, mismatched int) {
	t.batches++
	t.inserted += len(res.Inserted)
	t.present += len(res.AlreadyPresent)
	t.conflicts += len(res.Conflicts)
	t.renumbered += len(res.Renumbers)
	t.mismatched += mismatched
}

// readPendingBatch returns up to limit pending events in lamport order,
// leaving out the events push holds (MTIX-95.12): it is
// readPendingBatchFrom from the start of the queue.
//
// Delegates to the store so there is one pending-queue projection shared
// with the e2e harness. The projection previously lived here and omitted
// uid, which silently defeated the hub's same-logical-node no-op
// (MTIX-91); the e2e "mirror" of this function had uid, so tests passed
// while the CLI failed. Do not re-inline it.
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

// readPendingBatchFrom returns up to limit pending events after the cursor,
// in (lamport, event id) order, leaving out the events push holds
// (sync_quarantine source push, MTIX-95.12): a held event would otherwise
// stay at the head of the queue and, once a batch was full of them, stop
// every later event from being read. pushLoop reads on past each batch with
// the cursor, so held events at the head of the queue are skipped once per
// push rather than once per batch.
//
// The read is the store's single push projection
// (sqlite.Store.ReadPendingEventsAfter), which carries each event's uid
// (MTIX-91). Do not re-inline it.
func readPendingBatchFrom(ctx context.Context, store *sqlite.Store, after pendingCursor, limit int) ([]*model.SyncEvent, error) {
	return store.ReadPendingEventsAfter(ctx, after.lamport, after.eventID, limit)
}

// pushBatch sends one batch of valid events to the hub, marks the accepted
// ones pushed and drains the renumber-required outcomes (ADR-003 §6,
// MTIX-30.7): the hub rejected those creates because their number is
// already held, so the next free sibling is re-claimed, the node renumbered
// locally and the create re-queued ('pending') so a later batch carries a
// distinct number. batch numbers the batch in errors.
//
// The accepted events are the ones the hub inserted and the ones it already
// held (MTIX-95.3): an event whose earlier push committed on the hub but
// was never marked here, because the mark failed or the process stopped,
// is marked by the next push. An event whose id the hub holds with another
// node, op or payload is never marked: it is held (holdMismatches). It
// returns the hub's result and how many events it newly held.
func pushBatch(ctx context.Context, stderr io.Writer, pool eventPusher, store *sqlite.Store,
	events []*model.SyncEvent, batch int,
) (transport.PushResult, int, error) {
	res, err := pool.PushEventsResult(ctx, events)
	if err != nil {
		return transport.PushResult{}, 0, fmt.Errorf("push batch %d: %w", batch, err)
	}
	if markErr := markPushed(ctx, store, res.Accepted()); markErr != nil {
		return transport.PushResult{}, 0, fmt.Errorf("mark pushed batch %d: %w", batch, markErr)
	}
	mismatched, err := holdMismatches(ctx, stderr, store, events, res.Mismatches)
	if err != nil {
		return transport.PushResult{}, 0, fmt.Errorf("hold mismatched events of batch %d: %w", batch, err)
	}
	for _, r := range res.Renumbers {
		if _, err := store.RenumberForHubRejection(ctx, r.EventID); err != nil {
			return transport.PushResult{}, 0, fmt.Errorf("resolve renumber for %s (batch %d): %w", r.EventID, batch, err)
		}
	}
	return res, mismatched, nil
}

// markPushedFailKey is the context key of a test seam (MTIX-95.3): a
// func([]string) error under it runs before markPushed writes, with the ids
// to mark, and an error it returns is markPushed's, as a local failure
// after the hub committed the batch would be (a crash, a full disk). Push
// never sets it.
type markPushedFailKey struct{}

// markPushed updates sync_status from 'pending' to 'pushed' for every
// event_id the hub accepted. Done in a single tx for atomicity; if
// the UPDATE fails, the events remain pending and the next push tries
// again (idempotent on the hub side via ON CONFLICT DO NOTHING).
func markPushed(ctx context.Context, store *sqlite.Store, acceptedIDs []string) error {
	if len(acceptedIDs) == 0 {
		return nil
	}
	if fail, ok := ctx.Value(markPushedFailKey{}).(func([]string) error); ok {
		if err := fail(acceptedIDs); err != nil {
			return err
		}
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
