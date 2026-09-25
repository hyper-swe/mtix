// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// The clone's checks (MTIX-95.11). `mtix sync clone` stays all-or-nothing
// (it has no quarantine), but it runs the pull's checks (checkPulledEvent:
// the Lamport clock, then the FR-18.7 envelope, a hub row that did not
// decode included) on every hub event, and refuses the clone when any
// fails. Without this a clone applied, and adopted the clock of, an event
// that pull had quarantined, and the hub then refused every push. The
// refusal names the event and the reason and gives the recovery
// (cloneRecovery): on a fresh store, `mtix sync pull`, which quarantines
// the event and applies the rest.

// errCloneRefused marks a clone refused because a hub event fails the
// pull's checks.
var errCloneRefused = errors.New("clone refused")

// cloneRecovery is the recovery a clone refusal gives (MTIX-95.11). A clone
// normally runs on a fresh store, where `mtix sync pull` alone recovers.
// `mtix sync reconcile --discard-local --yes` deletes local tasks and
// unpushed changes (its dry run shows only the node count), so it is named
// only for a store that already holds sync state, after a push, a pending
// count of 0 and a human's go-ahead.
const cloneRecovery = "Clone has no quarantine: on a fresh store, run 'mtix sync pull' instead, which " +
	"quarantines the event and applies the rest. Only on a store that already holds sync state does " +
	"'mtix sync reconcile --discard-local --yes' come first; it deletes local tasks and unpushed changes, so " +
	"before it run 'mtix sync push', check that 'mtix sync status' shows pending 0, and get a human's go-ahead"

// cloneRefusal is the error of a clone refused on e. written says whether
// earlier batches were already applied (an event pushed to the hub during
// the clone); a refusal by the check before the clone wrote nothing.
func cloneRefusal(e *model.SyncEvent, reason error, written bool) error {
	state := "nothing was written"
	if written {
		state = "the batches before it were applied"
	}
	return fmt.Errorf("%w: hub event %s fails the checks sync pull runs (%s); %s. "+cloneRecovery,
		errCloneRefused, oneLine(e.EventID), quarantineReason(reason), state)
}

// preflightClone runs, before a clone writes anything, the pull's checks on
// every hub event the clone will apply: it pages through PullEvents from
// the keyset position after, batchSize at a time, each page after the
// previous page's last event (MTIX-95.4), with the running clock the clone
// would have (local, then the highest clock checked so far, since the clone
// applies in Lamport order). It returns an error wrapping errCloneRefused
// for the first event that fails, a page that does not advance past the
// cursor as an error naming the cursor (pageCursor.advance), and a hub
// error as it is. The clone therefore reads the hub's event log twice: once
// to check, once to apply.
func preflightClone(ctx context.Context, in pullIngest, hub cursorPuller,
	after transport.PullCursor, local int64, batchSize int,
) error {
	clock := local
	page := newPageCursor(after)
	for {
		events, hasMore, err := hub.PullEvents(ctx, page.at, batchSize)
		if err != nil {
			return fmt.Errorf("check events after %d: %w", page.at.Lamport, err)
		}
		if err := requireEvents(events); err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		if err := page.advance(events); err != nil {
			return fmt.Errorf("check events: %w", err)
		}
		for _, e := range events {
			if refused := checkPulledEvent(in, e, clock); refused != nil {
				return cloneRefusal(e, refused, false)
			}
			clock = max(clock, e.LamportClock)
		}
		if !hasMore {
			return nil
		}
	}
}

// checkThenClone checks every hub event (preflightClone) before the clone
// writes anything, then resets the pull state and runs cloneLoop. stage
// names the step that failed, for the "mtix sync <stage>: ..." message.
func checkThenClone(ctx context.Context, stderr io.Writer, pool cursorPuller,
	since transport.PullCursor, batchSize int,
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
