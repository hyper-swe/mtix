// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"sync"
	"time"
)

// Activity-window phases (ADR-006 §6, D18): fast contact right after
// activity, slower contact until the window closes, then none.
const (
	// HubContactFastInterval is the contact cadence in the fast phase.
	HubContactFastInterval = 5 * time.Second
	// HubContactFastWindow is how long after activity the fast phase lasts.
	HubContactFastWindow = 10 * time.Minute
	// HubContactSlowInterval is the contact cadence in the slow phase.
	HubContactSlowInterval = 60 * time.Second
	// HubContactSlowWindow is how long after activity contact stops.
	HubContactSlowWindow = 30 * time.Minute
)

// HubContactClockSkew is how far ahead of the clock a recorded activity may
// be before it is treated as stale (a clock stepped back, or a hand edit)
// rather than as fresh activity.
const HubContactClockSkew = 5 * time.Minute

// ErrContactInputs marks an unreadable scheduler input; the scheduler then
// fails safe and makes no hub contact.
var ErrContactInputs = errors.New("hub contact inputs unavailable")

// ContactInputs is one snapshot of everything the scheduler decides from.
type ContactInputs struct {
	Mode         HubContactMode
	Listen       ListenSettings
	LastActivity time.Time // zero: no activity recorded
	LiveLease    bool      // at least one live listener lease
}

// ContactDecision is the scheduler's answer for one pass.
type ContactDecision struct {
	Contact      bool
	Reason       string // manual|idle|activity-fast|activity-slow|interval|listen-hold|inputs-unavailable
	HoldDeadline time.Time
}

// ContactScheduler decides when a per-project daemon pass may contact the
// hub (MTIX-112.20.1). It reads its inputs fresh on every call, uses only
// the injected clock, and never contacts the hub itself: the caller dials
// when Decide says so and reports it with MarkContact. Safe for concurrent use.
type ContactScheduler struct {
	clock func() time.Time
	load  func() (ContactInputs, error)

	mu          sync.Mutex
	lastContact time.Time
}

// NewContactScheduler builds a scheduler from an injected clock and loader.
func NewContactScheduler(clock func() time.Time, load func() (ContactInputs, error)) *ContactScheduler {
	return &ContactScheduler{clock: clock, load: load}
}

// MarkContact records that a hub contact happened now.
func (s *ContactScheduler) MarkContact() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastContact = s.clock()
}

// Decide reports whether the hub may be contacted on this pass.
func (s *ContactScheduler) Decide() ContactDecision {
	in, err := s.load()
	if err != nil {
		return ContactDecision{Reason: "inputs-unavailable"}
	}
	now := s.clock()
	d, reason, deadline := contactCadence(in, now)
	dec := ContactDecision{Reason: reason, HoldDeadline: deadline}
	if d <= 0 {
		return dec
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dec.Contact = s.lastContact.IsZero() || now.Sub(s.lastContact) >= d
	return dec
}

// Delay is how long the pass loop should wait before the next pass: the
// local tick (mirror, relay and hook dispatch never depend on the hub),
// shortened when a hub contact is due sooner.
func (s *ContactScheduler) Delay(local time.Duration) time.Duration {
	in, err := s.load()
	if err != nil {
		return local
	}
	now := s.clock()
	d, _, _ := contactCadence(in, now)
	if d <= 0 {
		return local
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastContact.IsZero() {
		return minDuration(local, time.Millisecond*100)
	}
	due := s.lastContact.Add(d).Sub(now)
	return minDuration(local, maxDuration(due, 100*time.Millisecond))
}

// contactCadence returns the contact period now (0 means no contact), the
// reason, and the listen-hold deadline when a hold is armed.
func contactCadence(in ContactInputs, now time.Time) (time.Duration, string, time.Time) {
	if in.Mode.Kind == HubContactManual {
		return 0, "manual", time.Time{}
	}
	d, reason := baseCadence(in, now)
	hold, deadline := listenHold(in, now)
	if hold && (d <= 0 || in.Listen.Interval < d) {
		return in.Listen.Interval, "listen-hold", deadline
	}
	if d <= 0 {
		return 0, "idle", deadline
	}
	return d, reason, deadline
}

func baseCadence(in ContactInputs, now time.Time) (time.Duration, string) {
	if in.Mode.Kind == HubContactInterval {
		return in.Mode.Interval, "interval"
	}
	if in.LastActivity.IsZero() {
		return 0, "idle"
	}
	age := now.Sub(in.LastActivity)
	if age < -HubContactClockSkew {
		return 0, "idle" // future-dated: stale, never a fast phase
	}
	switch {
	case age < HubContactFastWindow:
		return HubContactFastInterval, "activity-fast"
	case age < HubContactSlowWindow:
		return HubContactSlowInterval, "activity-slow"
	default:
		return 0, "idle"
	}
}

// listenHold reports whether the listen hold is armed now: listening is on,
// a live lease exists, and the idle clamp has not passed since the last real
// activity. The clamp anchors on activity only: re-arming a lease never moves
// it, so a forgotten session cannot hold the hub awake indefinitely.
func listenHold(in ContactInputs, now time.Time) (bool, time.Time) {
	if !in.Listen.Hold || !in.LiveLease || in.LastActivity.IsZero() {
		return false, time.Time{}
	}
	if in.LastActivity.After(now.Add(HubContactClockSkew)) {
		return false, time.Time{} // future-dated: stale
	}
	deadline := in.LastActivity.Add(in.Listen.MaxIdle)
	return now.Before(deadline), deadline
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
