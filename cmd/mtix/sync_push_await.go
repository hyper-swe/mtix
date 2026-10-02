// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The creation gate of push (MTIX-95.37).
//
// INVARIANT: push sends no event about a task, about a task below it, or
// linking to it, while the creation of that task is still pending. The hub
// may reject the creation as a renumber, because a teammate holds the
// number; the task then moves to a new number and every pending event of it
// is re-addressed (Store.RenumberForHubRejection). An event already sent
// under the old number would reach the teammate's task. So a creation goes
// first, alone with the other events that wait for nothing, and the events
// that depend on it follow once the hub has accepted it (or are
// re-addressed and sent after the renumbered creation). A creation the hub
// blocks (a restore collision) or that push holds is never acknowledged, so
// what depends on it is never sent.
//
// pushLoop keeps a deferred event pending and reads it again: its cursor
// moves on past a batch, so it remembers where the first deferred event of
// a pass was read (awaitRedo) and goes back there when the pass ends.

// deferAwaitingCreation splits events, in order, into those push may send
// now and the number of those that wait for a pending creation (see
// Store.EventsAwaitingCreation). The deferred events stay pending.
func deferAwaitingCreation(ctx context.Context, store *sqlite.Store, events []*model.SyncEvent,
) ([]*model.SyncEvent, int, error) {
	waiting, err := store.EventsAwaitingCreation(ctx, events)
	if err != nil {
		return nil, 0, fmt.Errorf("find events awaiting a creation: %w", err)
	}
	if len(waiting) == 0 {
		return events, 0, nil
	}
	send := make([]*model.SyncEvent, 0, len(events)-len(waiting))
	for _, e := range events {
		if _, wait := waiting[e.EventID]; !wait {
			send = append(send, e)
		}
	}
	return send, len(events) - len(send), nil
}

// awaitRedo is pushLoop's memory of deferred events: redo is where the
// first batch that deferred an event was read, and progressed is whether any
// batch has since sent, renumbered or held an event, so reading it again can
// find a creation now acknowledged.
type awaitRedo struct {
	redo       *pendingCursor
	progressed bool
}

// note records that the batch read from start deferred deferred events.
func (a *awaitRedo) note(start pendingCursor, deferred int) {
	if deferred > 0 && a.redo == nil {
		a.redo = &start
	}
}

// back returns the cursor to read again from, and true, when events were
// deferred and something moved since; it then forgets them. A pass that moved
// nothing does not go back, so the loop ends.
func (a *awaitRedo) back() (pendingCursor, bool) {
	if a.redo == nil || !a.progressed {
		return pendingCursor{}, false
	}
	at := *a.redo
	a.redo, a.progressed = nil, false
	return at, true
}

// reset forgets deferred events: the loop reads from the head again.
func (a *awaitRedo) reset() {
	a.redo, a.progressed = nil, false
}
