// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package attach holds the release-defined attach plumbing of ADR-007 §6.4-§6.5:
// the attach-protocol handshake, the shim, the refresh registry for
// release-defined artifacts and the dispatcher restart rules. Nothing here
// records or changes a grant.
package attach

// Protocol is the attach protocol this binary speaks (ADR-007 §6.5). Harness
// glue passes the protocol it was written for on every attach verb.
const Protocol = 1

// Decision is the outcome of the attach-protocol handshake.
type Decision int

// Handshake outcomes (ADR-007 §6.5).
const (
	// DecisionServe serves the current protocol.
	DecisionServe Decision = iota + 1
	// DecisionServeAndRefresh serves protocol N-1 and marks the glue for refresh.
	DecisionServeAndRefresh
	// DecisionOutOfRange degrades the session to inbox-only and raises an alert.
	DecisionOutOfRange
)

// String names a decision for logs and tests.
func (d Decision) String() string {
	switch d {
	case DecisionServe:
		return "serve"
	case DecisionServeAndRefresh:
		return "serve-and-refresh"
	case DecisionOutOfRange:
		return "out-of-range"
	default:
		return "unknown"
	}
}

// SupportedRange returns the protocols a binary at current serves:
// [max(1, current-1), current]. Protocol 0 never existed, so the first
// protocol has no N-1.
func SupportedRange(current int) (lo, hi int) {
	lo = current - 1
	if lo < 1 {
		lo = 1
	}
	return lo, current
}

// Negotiate decides how a binary speaking current answers glue written for
// requested (ADR-007 §6.5): N is served, N-1 is served and refreshed, and
// anything else is out of range.
func Negotiate(current, requested int) Decision {
	lo, hi := SupportedRange(current)
	switch {
	case current < 1 || requested < lo || requested > hi:
		return DecisionOutOfRange
	case requested == hi:
		return DecisionServe
	default:
		return DecisionServeAndRefresh
	}
}

// Harness names accepted by the attach verbs; they match internal/procinfo.
const (
	HarnessClaudeCode = "claude-code"
	HarnessCodex      = "codex"
	HarnessPi         = "pi"
)

// Attach verbs called by harness glue (ADR-007 §6.5).
const (
	VerbSessionStart = "session-start"
	VerbPromptSubmit = "prompt-submit"
	VerbSessionEnd   = "session-end"
	VerbWait         = "wait"
)

// Harnesses lists the harnesses an attach verb may name.
func Harnesses() []string { return []string{HarnessClaudeCode, HarnessCodex, HarnessPi} }

// Verbs lists the attach verbs in the order the glue uses them.
func Verbs() []string {
	return []string{VerbSessionStart, VerbPromptSubmit, VerbSessionEnd, VerbWait}
}

// KnownHarness reports whether name is an accepted harness name.
func KnownHarness(name string) bool {
	for _, h := range Harnesses() {
		if h == name {
			return true
		}
	}
	return false
}
