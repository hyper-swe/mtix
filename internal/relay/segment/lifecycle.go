// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import "fmt"

// State is where a segment sits in the FR-21 lifecycle. The set is
// closed: a segment is the active tail, sealed behind a successor,
// pruned after retention, or condemned as corrupt.
type State uint8

// The segment lifecycle states (FR-21 §5.3, §5.4, §6.7).
const (
	// StateActive is the highest-numbered segment in a peer's
	// directory — the only file in the whole design that is ever
	// appended to, and the only one whose torn tail is normal.
	StateActive State = iota + 1

	// StateSealed is any segment with a successor. Sealed bytes are
	// immutable, which is what lets a reader treat damage there as
	// damage rather than as an append in flight.
	StateSealed

	// StatePruned is a sealed segment removed after every non-retired
	// peer acked past it and it aged past retention (§6.7). Terminal:
	// a peer that needs pruned history re-enters through bootstrap,
	// not by resurrecting the file.
	StatePruned

	// StateCorrupt is a segment whose bytes failed validation where an
	// in-flight append cannot explain it (§5.4). Terminal in this
	// layer: recovery mints new segments (republish, re-bootstrap)
	// rather than repairing this one, because in-place repair on a
	// bridged medium races a remote cache no writer can see (§5.5).
	StateCorrupt
)

// String renders the operator-facing name of a state.
func (s State) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateSealed:
		return "sealed"
	case StatePruned:
		return "pruned"
	case StateCorrupt:
		return "corrupt"
	default:
		return "unknown"
	}
}

// Valid reports whether s is a defined lifecycle state.
func (s State) Valid() bool {
	switch s {
	case StateActive, StateSealed, StatePruned, StateCorrupt:
		return true
	default:
		return false
	}
}

// Terminal reports whether no transition leaves s.
func (s State) Terminal() bool {
	switch s {
	case StatePruned, StateCorrupt:
		return true
	case StateActive, StateSealed:
		return false
	default:
		return false
	}
}

// CanTransitionTo reports whether s may become to.
//
// The permitted edges, and why the complement is refused:
//
//	active  → sealed   a successor segment now exists (rotation)
//	active  → corrupt  the header itself failed to validate
//	sealed  → pruned   retention plus acks allowed removal (§6.7)
//	sealed  → corrupt  immutable bytes failed to validate (§5.4)
//
// Sealed never returns to active: sealed bytes are immutable, and a
// writer that reopened one would be the second writer on a file the
// whole design is built to avoid. The active tail is never pruned
// directly — §6.7 prunes sealed segments only, so the tail cannot be
// removed out from under a reader mid-append. Pruned and corrupt are
// terminal; both recoveries create new segments instead.
func (s State) CanTransitionTo(to State) bool {
	if !s.Valid() || !to.Valid() {
		return false
	}
	switch s {
	case StateActive:
		return to == StateSealed || to == StateCorrupt
	case StateSealed:
		return to == StatePruned || to == StateCorrupt
	case StatePruned, StateCorrupt:
		return false
	default:
		return false
	}
}

// Transition applies a lifecycle edge, returning the new state or
// ErrInvalidTransition. It exists so callers cannot assign a state
// directly and skip the check.
func Transition(from, to State) (State, error) {
	if !from.CanTransitionTo(to) {
		return from, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, from, to)
	}
	return to, nil
}
