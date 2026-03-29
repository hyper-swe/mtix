// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestStateMachine_AllValidTransitions_NoError is an exhaustive table-driven test
// verifying every valid transition per FR-3.5.
func TestStateMachine_AllValidTransitions_NoError(t *testing.T) {
	tests := []struct {
		name string
		from model.Status
		to   model.Status
	}{
		// From open
		{"open→in_progress", model.StatusOpen, model.StatusInProgress},
		{"open→deferred", model.StatusOpen, model.StatusDeferred},
		{"open→cancelled", model.StatusOpen, model.StatusCancelled},
		{"open→blocked (auto)", model.StatusOpen, model.StatusBlocked},

		// From in_progress
		{"in_progress→done", model.StatusInProgress, model.StatusDone},
		{"in_progress→deferred", model.StatusInProgress, model.StatusDeferred},
		{"in_progress→cancelled", model.StatusInProgress, model.StatusCancelled},
		{"in_progress→open (unclaim)", model.StatusInProgress, model.StatusOpen},
		{"in_progress→blocked (auto)", model.StatusInProgress, model.StatusBlocked},

		// From blocked
		{"blocked→open (auto)", model.StatusBlocked, model.StatusOpen},
		{"blocked→in_progress (auto)", model.StatusBlocked, model.StatusInProgress},
		{"blocked→cancelled", model.StatusBlocked, model.StatusCancelled},

		// From done
		{"done→open (reopen)", model.StatusDone, model.StatusOpen},

		// From deferred
		{"deferred→open", model.StatusDeferred, model.StatusOpen},
		{"deferred→in_progress (claim)", model.StatusDeferred, model.StatusInProgress},
		{"deferred→cancelled", model.StatusDeferred, model.StatusCancelled},

		// From cancelled
		{"cancelled→open (reopen)", model.StatusCancelled, model.StatusOpen},

		// From invalidated
		{"invalidated→open (restore)", model.StatusInvalidated, model.StatusOpen},
		{"invalidated→cancelled", model.StatusInvalidated, model.StatusCancelled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateTransition(tt.from, tt.to)
			assert.NoError(t, err,
				"transition %s→%s should be valid", tt.from, tt.to)
		})
	}
}

// TestStateMachine_AllInvalidTransitions_ReturnsError is an exhaustive table-driven test
// verifying every invalid transition per FR-3.5.
func TestStateMachine_AllInvalidTransitions_ReturnsError(t *testing.T) {
	tests := []struct {
		name string
		from model.Status
		to   model.Status
	}{
		// From open — invalid targets (open→invalidated is valid with auto_only constraint)
		{"open→done", model.StatusOpen, model.StatusDone},

		// From blocked — invalid targets (blocked→invalidated is valid with auto_only)
		{"blocked→done", model.StatusBlocked, model.StatusDone},
		{"blocked→deferred", model.StatusBlocked, model.StatusDeferred},

		// From done — invalid targets (done→invalidated is valid with auto_only)
		{"done→in_progress", model.StatusDone, model.StatusInProgress},
		{"done→blocked", model.StatusDone, model.StatusBlocked},
		{"done→deferred", model.StatusDone, model.StatusDeferred},
		{"done→cancelled", model.StatusDone, model.StatusCancelled},

		// From deferred — invalid targets (deferred→invalidated is valid with auto_only)
		{"deferred→done", model.StatusDeferred, model.StatusDone},
		{"deferred→blocked", model.StatusDeferred, model.StatusBlocked},

		// From cancelled — invalid targets (cancelled→invalidated is valid with auto_only)
		{"cancelled→in_progress", model.StatusCancelled, model.StatusInProgress},
		{"cancelled→blocked", model.StatusCancelled, model.StatusBlocked},
		{"cancelled→done", model.StatusCancelled, model.StatusDone},
		{"cancelled→deferred", model.StatusCancelled, model.StatusDeferred},

		// From invalidated — invalid targets
		// (invalidated→open, in_progress, deferred are valid with requires_restore)
		// (invalidated→cancelled is valid with no constraint)
		{"invalidated→done", model.StatusInvalidated, model.StatusDone},
		{"invalidated→blocked", model.StatusInvalidated, model.StatusBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateTransition(tt.from, tt.to)
			assert.ErrorIs(t, err, model.ErrInvalidTransition,
				"transition %s→%s should be invalid", tt.from, tt.to)
		})
	}
}

// TestStateMachine_InvalidatedToDone_IsInvalid verifies the key constraint
// that invalidated nodes cannot go directly to done (FR-3.5).
func TestStateMachine_InvalidatedToDone_IsInvalid(t *testing.T) {
	err := model.ValidateTransition(model.StatusInvalidated, model.StatusDone)
	assert.ErrorIs(t, err, model.ErrInvalidTransition,
		"invalidated→done must be invalid — forces re-evaluation via restore/rerun first")
}

// TestStateMachine_DoneToOpen_RequiresReopen verifies done→open requires reopen (FR-3.5).
func TestStateMachine_DoneToOpen_RequiresReopen(t *testing.T) {
	// The transition itself is valid...
	err := model.ValidateTransition(model.StatusDone, model.StatusOpen)
	assert.NoError(t, err)

	// ...but it requires the reopen constraint.
	constraint := model.TransitionConstraintFor(model.StatusDone, model.StatusOpen)
	assert.Equal(t, model.ConstraintRequiresReopen, constraint,
		"done→open must require reopen")
}

// TestStateMachine_BlockedIsAutoManagedOnly verifies blocked transitions are auto-only (FR-3.8).
func TestStateMachine_BlockedIsAutoManagedOnly(t *testing.T) {
	// open→blocked is auto-only
	assert.True(t, model.IsAutoManagedTransition(model.StatusOpen, model.StatusBlocked),
		"open→blocked should be auto-managed only")

	// in_progress→blocked is auto-only
	assert.True(t, model.IsAutoManagedTransition(model.StatusInProgress, model.StatusBlocked),
		"in_progress→blocked should be auto-managed only")

	// blocked→open is auto-only (blocker resolution)
	assert.True(t, model.IsAutoManagedTransition(model.StatusBlocked, model.StatusOpen),
		"blocked→open should be auto-managed only")

	// blocked→in_progress is auto-only (blocker resolution)
	assert.True(t, model.IsAutoManagedTransition(model.StatusBlocked, model.StatusInProgress),
		"blocked→in_progress should be auto-managed only")
}

// TestStateMachine_IdempotentTransition_NoError verifies same-status transitions
// are treated as no-ops per FR-7.7a.
func TestStateMachine_IdempotentTransition_NoError(t *testing.T) {
	for _, status := range model.AllStatuses() {
		t.Run(string(status), func(t *testing.T) {
			err := model.ValidateTransition(status, status)
			assert.NoError(t, err,
				"idempotent transition %s→%s should be a no-op", status, status)
		})
	}
}

// TestStateMachine_InProgressToOpen_RequiresUnclaim verifies the unclaim constraint.
func TestStateMachine_InProgressToOpen_RequiresUnclaim(t *testing.T) {
	constraint := model.TransitionConstraintFor(model.StatusInProgress, model.StatusOpen)
	assert.Equal(t, model.ConstraintRequiresUnclaim, constraint,
		"in_progress→open must require unclaim with reason")
}

// TestStateMachine_CancelledToOpen_RequiresReopen verifies the reopen constraint.
func TestStateMachine_CancelledToOpen_RequiresReopen(t *testing.T) {
	constraint := model.TransitionConstraintFor(model.StatusCancelled, model.StatusOpen)
	assert.Equal(t, model.ConstraintRequiresReopen, constraint,
		"cancelled→open must require reopen")
}

// TestStateMachine_InvalidatedToOpen_RequiresRestore verifies the restore constraint.
func TestStateMachine_InvalidatedToOpen_RequiresRestore(t *testing.T) {
	constraint := model.TransitionConstraintFor(model.StatusInvalidated, model.StatusOpen)
	assert.Equal(t, model.ConstraintRequiresRestore, constraint,
		"invalidated→open must require restore or rerun")
}

// TestStateMachine_DeferredToInProgress_RequiresClaim verifies the claim constraint.
func TestStateMachine_DeferredToInProgress_RequiresClaim(t *testing.T) {
	constraint := model.TransitionConstraintFor(model.StatusDeferred, model.StatusInProgress)
	assert.Equal(t, model.ConstraintRequiresClaim, constraint,
		"deferred→in_progress must require claim")
}
