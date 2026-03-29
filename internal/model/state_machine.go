// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// TransitionConstraint describes conditions for a state transition.
type TransitionConstraint string

const (
	// ConstraintNone means no special constraint.
	ConstraintNone TransitionConstraint = ""

	// ConstraintRequiresReopen means the transition must go through mtix reopen.
	ConstraintRequiresReopen TransitionConstraint = "requires_reopen"

	// ConstraintRequiresUnclaim means the transition must go through mtix unclaim with reason.
	ConstraintRequiresUnclaim TransitionConstraint = "requires_unclaim"

	// ConstraintAutoOnly means the transition is system-managed only (FR-3.8).
	ConstraintAutoOnly TransitionConstraint = "auto_only"

	// ConstraintRequiresRestore means the transition must go through mtix restore or mtix rerun.
	ConstraintRequiresRestore TransitionConstraint = "requires_restore"

	// ConstraintRequiresClaim means the transition must go through mtix claim.
	ConstraintRequiresClaim TransitionConstraint = "requires_claim"
)

// transitionEntry defines a valid state transition with optional constraint.
type transitionEntry struct {
	To         Status
	Constraint TransitionConstraint
}

// validTransitions defines all valid state transitions per FR-3.5.
// The key is the source status, the value is a list of valid targets.
var validTransitions = map[Status][]transitionEntry{
	StatusOpen: {
		{To: StatusInProgress, Constraint: ConstraintNone},
		{To: StatusDeferred, Constraint: ConstraintNone},
		{To: StatusCancelled, Constraint: ConstraintNone},
		{To: StatusBlocked, Constraint: ConstraintAutoOnly},
		{To: StatusInvalidated, Constraint: ConstraintAutoOnly},
	},
	StatusInProgress: {
		{To: StatusDone, Constraint: ConstraintNone},
		{To: StatusDeferred, Constraint: ConstraintNone},
		{To: StatusCancelled, Constraint: ConstraintNone},
		{To: StatusOpen, Constraint: ConstraintRequiresUnclaim},
		{To: StatusBlocked, Constraint: ConstraintAutoOnly},
		{To: StatusInvalidated, Constraint: ConstraintAutoOnly},
	},
	StatusBlocked: {
		// Auto-restore to previous_status when all blockers resolve.
		{To: StatusOpen, Constraint: ConstraintAutoOnly},
		{To: StatusInProgress, Constraint: ConstraintAutoOnly},
		{To: StatusCancelled, Constraint: ConstraintNone},
		{To: StatusInvalidated, Constraint: ConstraintAutoOnly},
	},
	StatusDone: {
		{To: StatusOpen, Constraint: ConstraintRequiresReopen},
		{To: StatusInvalidated, Constraint: ConstraintAutoOnly},
	},
	StatusDeferred: {
		{To: StatusOpen, Constraint: ConstraintNone},
		{To: StatusInProgress, Constraint: ConstraintRequiresClaim},
		{To: StatusCancelled, Constraint: ConstraintNone},
		{To: StatusInvalidated, Constraint: ConstraintAutoOnly},
	},
	StatusCancelled: {
		{To: StatusOpen, Constraint: ConstraintRequiresReopen},
		{To: StatusInvalidated, Constraint: ConstraintAutoOnly},
	},
	StatusInvalidated: {
		{To: StatusOpen, Constraint: ConstraintRequiresRestore},
		{To: StatusInProgress, Constraint: ConstraintRequiresRestore},
		{To: StatusDeferred, Constraint: ConstraintRequiresRestore},
		{To: StatusCancelled, Constraint: ConstraintNone},
	},
}

// ValidateTransition checks whether a status transition is valid per FR-3.5.
// Returns nil if the transition is valid, ErrInvalidTransition otherwise.
// This function checks only the state machine rules — it does not enforce
// constraint semantics (like requiring reason text for unclaim).
func ValidateTransition(from, to Status) error {
	if from == to {
		// Idempotent — same status is a no-op, not an error (FR-7.7a).
		return nil
	}

	entries, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf(
			"no transitions defined from status %q: %w",
			from, ErrInvalidTransition,
		)
	}

	for _, entry := range entries {
		if entry.To == to {
			return nil
		}
	}

	return fmt.Errorf(
		"transition from %q to %q is not allowed: %w",
		from, to, ErrInvalidTransition,
	)
}

// TransitionConstraintFor returns the constraint for a given transition.
// Returns ConstraintNone if the transition is not found.
func TransitionConstraintFor(from, to Status) TransitionConstraint {
	entries, ok := validTransitions[from]
	if !ok {
		return ConstraintNone
	}

	for _, entry := range entries {
		if entry.To == to {
			return entry.Constraint
		}
	}

	return ConstraintNone
}

// IsAutoManagedTransition returns true if the transition is auto-only (FR-3.8).
// Auto-managed transitions are handled by the system, not by user commands.
func IsAutoManagedTransition(from, to Status) bool {
	return TransitionConstraintFor(from, to) == ConstraintAutoOnly
}

// TransitionInfo describes a single state transition for introspection per FR-13.2.
type TransitionInfo struct {
	From       Status               `json:"from"`
	To         Status               `json:"to"`
	Constraint TransitionConstraint `json:"constraint,omitempty"`
}

// GetAllTransitions returns all valid state transitions for documentation generation.
// Used by the DocGen engine to produce STATUS_MACHINE.md per FR-13.2.
func GetAllTransitions() []TransitionInfo {
	var result []TransitionInfo
	for from, entries := range validTransitions {
		for _, entry := range entries {
			result = append(result, TransitionInfo{
				From:       from,
				To:         entry.To,
				Constraint: entry.Constraint,
			})
		}
	}
	return result
}

