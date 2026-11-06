// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// runSyncPullObserved exposes failures before CLI formatting (including hook
// mode's warning-and-continue) without changing the ordinary pull's behavior.
func runSyncPullObserved(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, limit int, onFailure func(error),
) (err error) {
	fail := func(stage string, cause error) error {
		if onFailure != nil {
			onFailure(cause)
		}
		return wrapSyncErr(stderr, stage, cause)
	}
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync pull: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync pull: local store not initialized")
	}
	if limit <= 0 {
		limit = pullDefaultBatchSize
	}
	svc := exchangeService(app.store)
	owner, err := svc.BeginExchangePull(ctx, app.mtixDir)
	if err != nil {
		return fail("pull sync lock", err)
	}
	defer owner.Close()
	completionAttempted := false
	defer func() {
		if err != nil && !completionAttempted {
			err = errors.Join(err, owner.Incomplete(ctx))
		}
	}()
	in := newPullIngest(stderr)
	in.receipt = newPullExchangeTracker(svc.MaxImportSize)
	res, stopped, err := daemonObservedPullPhases(ctx, stderr, args, opts, limit, in, fail)
	if stopped {
		return err
	}

	completionAttempted = true
	if err := completePullExchange(ctx, owner, in.receipt, res); err != nil {
		return err
	}
	fmt.Fprintf(stdout,
		"pull complete: %d events applied across %d batches\n", res.pulled, res.batches)
	printLateEventSweep(stdout, res.sweep)
	printQuarantineHeld(ctx, stdout, stderr, app.store)
	return nil
}

// daemonObservedPullPhases keeps quarantine recovery before hub contact and
// bootstrap hook-floor updates in the same order as ordinary explicit pulls.
func daemonObservedPullPhases(ctx context.Context, stderr io.Writer, args []string,
	opts transport.Options, limit int, in pullIngest, fail func(string, error) error,
) (pullOutcome, bool, error) {

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
		return pullOutcome{}, true, fail("quarantine retry", err)
	}
	// The retry's events are part of the first fill too; set the floor now,
	// before a hub failure below can return.
	initBootstrapHookFloor(ctx, stderr, bootstrap && first.applied > 0)

	res, err := connectAndPull(ctx, in, args, opts, limit)
	if err != nil {
		return res, true, fail(res.stage, err)
	}
	initBootstrapHookFloor(ctx, stderr, bootstrap && res.pulled+res.retried > 0)

	return res, false, nil
}
