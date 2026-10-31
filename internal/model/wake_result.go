// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

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
