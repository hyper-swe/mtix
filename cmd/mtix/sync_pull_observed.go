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

// syncPullInput groups one invocation without changing CLI arguments (MTIX-112.20.7).
type syncPullInput struct {
	stdout, stderr io.Writer
	args           []string
	opts           transport.Options
	limit          int
	onOutcome      func(daemonPullOutcome)
	onConnect      func(context.Context)
}

// pullObserver retains raw causes before hook formatting discards them (MTIX-112.20.7).
type pullObserver struct {
	onFinish  func(error)
	onFailure func(error)
	onConnect func(context.Context)
}

// fail preserves existing formatting and hook-mode early-return behavior (MTIX-112.20.7).
func (o pullObserver) fail(stderr io.Writer, stage string, cause error) error {
	if o.onFailure != nil {
		o.onFailure(cause)
	}
	return wrapSyncErr(stderr, stage, cause)
}

// runSyncPullObserved exposes existing pull boundaries without changing its effects (MTIX-112.20.7).
func runSyncPullObserved(ctx context.Context, req syncPullInput, observer pullObserver) (err error) {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync pull: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync pull: local store not initialized")
	}
	if req.limit <= 0 {
		req.limit = pullDefaultBatchSize
	}
	svc := exchangeService(app.store)
	owner, err := svc.BeginExchangePull(ctx, app.mtixDir)
	if err != nil {
		return observer.fail(req.stderr, "pull sync lock", err)
	}
	defer owner.Close()
	// Publish while exchange ownership is held, after incomplete cleanup (MTIX-112.20.7).
	defer func() {
		if observer.onFinish != nil {
			observer.onFinish(err)
		}
	}()
	completionAttempted := false
	defer func() {
		if err != nil && !completionAttempted {
			err = errors.Join(err, owner.Incomplete(ctx))
		}
	}()
	in := newPullIngest(req.stderr)
	in.receipt = newPullExchangeTracker(svc.MaxImportSize)
	in.onConnect = observer.onConnect
	res, stopped, err := daemonObservedPullPhases(ctx, req, in, observer)
	if stopped {
		return err
	}
	completionAttempted = true
	if err := completePullExchange(ctx, owner, in.receipt, res); err != nil {
		return err
	}
	fmt.Fprintf(req.stdout, "pull complete: %d events applied across %d batches\n", res.pulled, res.batches)
	printLateEventSweep(req.stdout, res.sweep)
	printQuarantineHeld(ctx, req.stdout, req.stderr, app.store)
	return nil
}

// daemonObservedPullPhases preserves quarantine-before-contact and hook floors (MTIX-112.20.7).
func daemonObservedPullPhases(ctx context.Context, req syncPullInput, in pullIngest, observer pullObserver) (pullOutcome, bool, error) {
	// Bootstrap history is detected before either quarantine retry or hub ingest.
	preTail, tailErr := app.store.JournalTail(ctx)
	bootstrap := tailErr == nil && preTail == 0
	first, err := retryQuarantinedEvents(ctx, in, app.store, req.limit)
	if err != nil {
		return pullOutcome{}, true, observer.fail(req.stderr, "quarantine retry", err)
	}
	initBootstrapHookFloor(ctx, req.stderr, bootstrap && first.applied > 0)
	res, err := connectAndPull(ctx, in, req.args, req.opts, req.limit)
	if err != nil {
		return res, true, observer.fail(req.stderr, res.stage, err)
	}
	initBootstrapHookFloor(ctx, req.stderr, bootstrap && res.pulled+res.retried > 0)
	return res, false, nil
}
