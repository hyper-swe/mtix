// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/model"
)

// The clone's checks (MTIX-95.11). `mtix sync clone` stays all-or-nothing
// (it has no quarantine), but it runs the pull's checks (checkPulledEvent:
// the Lamport clock, then the FR-18.7 envelope, a hub row that did not
// decode included) on every hub event, and refuses the clone when any
// fails. Without this a clone applied, and adopted the clock of, an event
// that pull had quarantined, and the hub then refused every push. The
// refusal names the event and the reason and points to `mtix sync
// reconcile --discard-local --yes` then `mtix sync pull`, which quarantines
// the event and applies the rest.

// errCloneRefused marks a clone refused because a hub event fails the
// pull's checks.
var errCloneRefused = errors.New("clone refused")

// cloneRefusal is the error of a clone refused on e. written says whether
// earlier batches were already applied (an event pushed to the hub during
// the clone); a refusal by the check before the clone wrote nothing.
func cloneRefusal(e *model.SyncEvent, reason error, written bool) error {
	state := "nothing was written"
	if written {
		state = "the batches before it were applied"
	}
	return fmt.Errorf(
		"%w: hub event %s fails the checks sync pull runs (%s); %s. Run 'mtix sync reconcile --discard-local --yes', "+
			"then 'mtix sync pull', which quarantines the event and applies the rest",
		errCloneRefused, oneLine(e.EventID), quarantineReason(reason), state)
}

// preflightClone runs, before a clone writes anything, the pull's checks on
// every hub event the clone will apply: it pages through PullEvents from
// since, batchSize at a time, with the running clock the clone would have
// (local, then the highest clock checked so far, since the clone applies in
// Lamport order). It returns an error wrapping errCloneRefused for the
// first event that fails, and a hub error as it is. The clone therefore
// reads the hub's event log twice: once to check, once to apply.
func preflightClone(ctx context.Context, in pullIngest, hub cursorPuller,
	since, local int64, batchSize int,
) error {
	clock := local
	for {
		events, hasMore, err := hub.PullEvents(ctx, since, batchSize)
		if err != nil {
			return fmt.Errorf("check events after %d: %w", since, err)
		}
		if err := requireEvents(events); err != nil {
			return err
		}
		for _, e := range events {
			if refused := checkPulledEvent(in, e, clock); refused != nil {
				return cloneRefusal(e, refused, false)
			}
			clock = max(clock, e.LamportClock)
			since = max(since, e.LamportClock)
		}
		if len(events) == 0 || !hasMore {
			return nil
		}
	}
}

// checkThenClone checks every hub event (preflightClone) before the clone
// writes anything, then resets the pull state and runs cloneLoop. stage
// names the step that failed, for the "mtix sync <stage>: ..." message.
func checkThenClone(ctx context.Context, stderr io.Writer, pool cursorPuller,
	since int64, batchSize int,
) (pulled, batches int, stage string, err error) {
	local, clockErr := app.store.LocalLamportClock(ctx)
	if clockErr != nil {
		return 0, 0, "read local clock", clockErr
	}
	if checkErr := preflightClone(ctx, newPullIngest(stderr), pool, since, local, batchSize); checkErr != nil {
		return 0, 0, "clone check", checkErr
	}
	// A clone rebuilds the store from the hub: the next pull's late-event
	// sweep diffs the full hub history (MTIX-95.5); no quarantine (MTIX-95.11).
	if resetErr := resetPullState(ctx, app.store); resetErr != nil {
		return 0, 0, "reset pull state", resetErr
	}
	pulled, batches, err = cloneLoop(ctx, stderr, pool, app.store, since, batchSize)
	return pulled, batches, "clone loop", err
}
