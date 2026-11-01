// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// Delivery outcome states recorded in the wake ledger by route adapters
// (MTIX-112.40 design note section 4; ADR-007 section 6.3). WakeDelivered and
// WakeError remain readable for rows written by earlier releases.
const (
	// WakeSent means an adapter handed the nudge to its route; delivery is not yet observed.
	WakeSent WakeOutcome = "sent"
	// WakeConfirmed means the target session was observed acting on the nudge.
	WakeConfirmed WakeOutcome = "confirmed"
	// WakeHeld means the route needs peer approval before it may deliver.
	WakeHeld WakeOutcome = "held"
	// WakeUndeliverable means no route could deliver; WakeResult.Reason says why.
	WakeUndeliverable WakeOutcome = "undeliverable"
	// WakeOff means live wake is switched off, so nothing was sent.
	WakeOff WakeOutcome = "off"
)

// WakeResult is the persisted result of one claimed delivery attempt.
type WakeResult struct {
	Outcome WakeOutcome
	// Route names the adapter that produced the outcome; empty for legacy rows.
	Route string
	// Reason is a short machine-readable cause, required for undeliverable.
	Reason string
	// At is the RFC3339Nano time the outcome was recorded; empty for legacy rows.
	At string
}

// Canonical maps the legacy outcome names onto the current state set at read time:
// delivered reads as sent and error as undeliverable. Stored rows are never rewritten.
func (o WakeOutcome) Canonical() WakeOutcome {
	switch o {
	case WakeDelivered:
		return WakeSent
	case WakeError:
		return WakeUndeliverable
	}
	return o
}

// Terminal reports whether the outcome closes the claim for its (registration, event).
func (o WakeOutcome) Terminal() bool {
	switch o {
	case WakeSent, WakeConfirmed, WakeHeld, WakeUndeliverable, WakeOff, WakeDelivered, WakeError:
		return true
	}
	return false
}

// WakeState is the sender-visible delivery state of an addressed message
// (MTIX-112.40.6, design note section 4). The inbox stays the source of truth: every
// state other than confirmed means the message is in the inbox and the wake is
// unconfirmed, never lost. unconfirmed is derived at read time and is never an inbox event.
type WakeState string

// The documented delivery states. WakeStates lists them; no other value is ever shown.
const (
	// WakeStateConfirmed means the target session was observed acting on the nudge.
	WakeStateConfirmed WakeState = "confirmed"
	// WakeStateHeld means the route needs peer approval; the event waits in the inbox.
	WakeStateHeld WakeState = "held"
	// WakeStateNoDispatcher means no dispatcher heartbeat exists, so nothing nudges: inbox only.
	WakeStateNoDispatcher WakeState = "no-dispatcher"
	// WakeStateNoLiveSession means the agent has no registered session on this host.
	WakeStateNoLiveSession WakeState = "no-live-session"
	// WakeStateNudged means a nudge was sent and the confirmation window is still open.
	WakeStateNudged WakeState = "nudged"
	// WakeStateOff means live wake is switched off for the agent, so nothing is sent.
	WakeStateOff WakeState = "off"
	// WakeStateQueued means a session and dispatcher exist and no nudge was recorded yet.
	WakeStateQueued WakeState = "queued"
	// WakeStateUndeliverable means no route could deliver; the reason says why.
	WakeStateUndeliverable WakeState = "undeliverable"
	// WakeStateUnconfirmed means a nudge was sent and the confirmation window passed unseen.
	WakeStateUnconfirmed WakeState = "unconfirmed"
	// WakeStateUnknown means the state could not be determined; the reason carries the cause.
	WakeStateUnknown WakeState = "unknown"
)

// WakeStates returns every documented delivery state in sorted order.
func WakeStates() []WakeState {
	return []WakeState{
		WakeStateConfirmed, WakeStateHeld, WakeStateNoDispatcher, WakeStateNoLiveSession, WakeStateNudged,
		WakeStateOff, WakeStateQueued, WakeStateUndeliverable, WakeStateUnconfirmed, WakeStateUnknown,
	}
}

// State maps a ledger outcome to its delivery state, with Canonical folding legacy names
// first. A sent nudge older than the confirmation window reads as unconfirmed. Every
// outcome maps to exactly one state; an unrecognized outcome is unknown, never dropped
// (MTIX-112.40.6, FR-20).
func (o WakeOutcome) State(age, window time.Duration) WakeState {
	switch o.Canonical() {
	case WakeSent:
		if age > window {
			return WakeStateUnconfirmed
		}
		return WakeStateNudged
	case WakeConfirmed:
		return WakeStateConfirmed
	case WakeHeld:
		return WakeStateHeld
	case WakeOff:
		return WakeStateOff
	case WakeUndeliverable:
		return WakeStateUndeliverable
	case WakeClaimed:
		return WakeStateQueued
	}
	return WakeStateUnknown
}
