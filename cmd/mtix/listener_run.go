// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

// daemonListener is the listen-mode behavior of `mtix daemon --listen`
// (MTIX-112.20.4): the scheduler's cadence while the listen hold is armed,
// and an exit as soon as nothing listens any more. It adds no timer and no
// dial of its own: every hub contact, the first pull included, goes through
// the ContactScheduler, so a board past the idle clamp (or with no recorded
// activity) is not contacted at arm time. A waiter that re-parks every few
// minutes therefore causes no hub dial until real activity re-arms the hold.
type daemonListener struct {
	load  func() (service.ContactInputs, error)
	now   func() time.Time
	hub   bool // a hub DSN is configured
	relay bool // a relay folder is configured
}

// Done reports whether the listener must exit and why. It is checked after
// every pass, so the process ends within one local tick of the last lease
// going, which is always inside one listen interval.
func (l *daemonListener) Done() (bool, string) {
	in, err := l.load()
	if err != nil {
		return true, "listener inputs unreadable (" + err.Error() + ")"
	}
	switch {
	case in.Mode.Kind == service.HubContactManual:
		return true, "sync.hub_contact is manual"
	case !in.Listen.Hold:
		return true, "sync.listen is off"
	case !in.LiveLease:
		return true, "no live waiter lease is left"
	}
	if l.relay {
		return false, "" // a relay is local disk: ingest while a waiter lives
	}
	if armed, _ := service.ListenHoldArmed(in, l.now()); !armed {
		return true, "the listen hold has ended (idle clamp or no recorded activity)"
	}
	return false, ""
}

// Bounds on how long a starting daemon or listener waits for a lifetime lock
// that a waiter or doctor probe holds for an instant (daemonLockHeld).
const (
	listenerOwnershipTries = 5
	listenerOwnershipWait  = 100 * time.Millisecond
)

// daemonOwnershipFor takes the daemon lifetime lock. Every daemon start
// retries briefly before concluding that another daemon owns the board,
// because a waiter or `mtix doctor` probes the same lock for an instant and
// must never turn a starting daemon away.
func daemonOwnershipFor(stderr io.Writer, dir string) (*daemonOwner, error) {
	for i := 0; i < listenerOwnershipTries; i++ {
		owner, err := acquireDaemonOwnership(dir)
		if !errors.Is(err, errDaemonLockHeld) {
			return owner, err
		}
		time.Sleep(listenerOwnershipWait)
	}
	return startDaemonOwnership(stderr, "mtix daemon", dir)
}

// newDaemonListener builds the listen-mode behavior for this board.
func newDaemonListener(hub bool, intervalSec int) (*daemonListener, error) {
	src, err := newHubContactSource(app.mtixDir)
	if err != nil {
		return nil, fmt.Errorf("mtix daemon --listen: %w", err)
	}
	src.grandfatherInterval = time.Duration(intervalSec) * time.Second
	return &daemonListener{load: src.snapshot, now: src.now, hub: hub, relay: relayDir() != ""}, nil
}
