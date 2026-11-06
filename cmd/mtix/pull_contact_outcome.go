// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
)

// executeSyncPull separates a transport outcome from local skips and shutdown (MTIX-112.20.7).
func executeSyncPull(ctx context.Context, req syncPullInput) (daemonPullOutcome, error) {
	outcome := daemonPullOutcome{ok: true}
	observeFailure := func(cause error) {
		attempted := outcome.attempted
		outcome = failedDaemonPull(cause)
		outcome.attempted = attempted
	}
	finished := false
	finish := func(err error) {
		finished = true
		if err != nil && outcome.ok {
			observeFailure(err)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome = daemonPullOutcome{}
			return
		}
		if outcome.attempted && req.onOutcome != nil {
			outcome.reported = true
			req.onOutcome(outcome)
		}
	}
	observer := pullObserver{onFailure: observeFailure, onFinish: finish, onConnect: func(contactCtx context.Context) {
		outcome.attempted = true
		if req.onConnect != nil {
			req.onConnect(contactCtx)
		}
	}}
	err := runSyncPullObserved(ctx, req, observer)
	if !finished {
		finish(err)
	}
	return outcome, err
}

// recordExplicitPullContact lets the printed manual recovery replace a stale result (MTIX-112.20.7).
func recordExplicitPullContact(stderr io.Writer, outcome daemonPullOutcome) {
	if !outcome.attempted {
		return
	}
	src, err := newHubContactSource(app.mtixDir)
	if err == nil {
		err = src.recordContact(src.now(), "pull", outcome)
	}
	if err != nil {
		warnSync(stderr, "mtix sync pull: contact outcome", err)
	}
}
