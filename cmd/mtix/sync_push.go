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
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	pushed, batches, conflicts, err := pushLoop(ctx, stderr, pool, app.store)
	if err != nil {
		return wrapSyncErr(stderr, "push loop", err)
	}

	fmt.Fprintf(stdout,
		"push complete: %d events pushed across %d batches; %d conflicts surfaced\n",
		pushed, batches, conflicts)
	return nil
}

// pushLoop reads pending events from the local sync_events log in
// batches and ships each batch through transport.PushEvents. After a
// successful push, accepted event_ids are marked sync_status='pushed'
// in a single tx so a crash mid-loop leaves the queue at a known
// state (still pending; the next push retries).
func pushLoop(ctx context.Context, stderr io.Writer,
	pool *transport.Pool, store *sqlite.Store,
) (totalPushed, batches, totalConflicts int, err error) {
	for {
		events, err := readPendingBatch(ctx, store, pushBatchSize)
		if err != nil {
			return totalPushed, batches, totalConflicts,
				fmt.Errorf("read pending batch %d: %w", batches+1, err)
		}
		if len(events) == 0 {
			return totalPushed, batches, totalConflicts, nil
		}
		acceptedIDs, conflicts, pushErr := pool.PushEvents(ctx, events)
		if pushErr != nil {
			return totalPushed, batches, totalConflicts,
				fmt.Errorf("push batch %d: %w", batches+1, pushErr)
		}
		if err := markPushed(ctx, store, acceptedIDs); err != nil {
			return totalPushed, batches, totalConflicts,
				fmt.Errorf("mark pushed batch %d: %w", batches+1, err)
		}
		totalPushed += len(acceptedIDs)
		totalConflicts += len(conflicts)
		batches++
		fmt.Fprintf(stderr, "push progress: batch %d (%d sent, %d accepted, %d conflicts)\n",
			batches, len(events), len(acceptedIDs), len(conflicts))
	}
}

// readPendingBatch returns up to limit events from sync_events in
// lamport order. Reads via readDB (no write tx needed).
func readPendingBatch(ctx context.Context, store *sqlite.Store, limit int) ([]*model.SyncEvent, error) {
	rows, err := store.Query(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending'
		ORDER BY lamport_clock ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*model.SyncEvent, 0, limit)
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
