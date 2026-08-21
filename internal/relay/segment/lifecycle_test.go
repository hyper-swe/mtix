// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"errors"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// allStates is the closed set of lifecycle states. The exhaustive
// transition tests below iterate its square, so adding a state without
// updating the transition table fails the build's own coverage of it.
var allStates = []segment.State{
	segment.StateActive,
	segment.StateSealed,
	segment.StatePruned,
	segment.StateCorrupt,
}

// TestState_String_CoversEverySegmentState pins the operator-facing
// spelling of every state plus the zero/unknown value.
func TestState_String_CoversEverySegmentState(t *testing.T) {
	tests := []struct {
		name  string
		state segment.State
		want  string
	}{
		{"active", segment.StateActive, "active"},
		{"sealed", segment.StateSealed, "sealed"},
		{"pruned", segment.StatePruned, "pruned"},
		{"corrupt", segment.StateCorrupt, "corrupt"},
		{"zero value", segment.State(0), "unknown"},
		{"out of range", segment.State(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.state.String())
		})
	}
}

// TestState_Valid_RejectsUnknownStates keeps the enum closed.
func TestState_Valid_RejectsUnknownStates(t *testing.T) {
	for _, s := range allStates {
		require.True(t, s.Valid(), "state %v must be valid", s)
	}
	require.False(t, segment.State(0).Valid())
	require.False(t, segment.State(99).Valid())
}

// TestStateMachine_AllValidTransitions is the FR-21 §5.3/§5.4/§6.7
// lifecycle, enumerated: active→sealed (a successor exists), sealed→
// pruned (retention plus acks), and the two damage edges into corrupt.
//
// Named per QUALITY-STANDARDS §3.6 scenario 1, whose canonical example
// is the node state machine; the segment lifecycle gets the same
// treatment because every layer above trusts these verdicts.
func TestStateMachine_AllValidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from segment.State
		to   segment.State
	}{
		{"active sealed by a successor segment", segment.StateActive, segment.StateSealed},
		{"active header damaged", segment.StateActive, segment.StateCorrupt},
		{"sealed pruned after retention", segment.StateSealed, segment.StatePruned},
		{"sealed damaged", segment.StateSealed, segment.StateCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, tt.from.CanTransitionTo(tt.to))
			got, err := segment.Transition(tt.from, tt.to)
			require.NoError(t, err)
			require.Equal(t, tt.to, got)
		})
	}
}

// TestStateMachine_AllInvalidTransitions walks the complement of the
// valid set over the full state square, so every rejected edge is
// covered without hand-listing it. The rejections that matter most:
// a sealed segment is immutable (never back to active), the active
// tail is never prunable (§6.7 prunes sealed segments only), pruned is
// terminal, and corrupt never silently heals (§5.4 — recovery is
// republish or re-bootstrap, both of which mint new segments rather
// than transitioning this one).
func TestStateMachine_AllInvalidTransitions(t *testing.T) {
	valid := map[segment.State]map[segment.State]bool{
		segment.StateActive: {segment.StateSealed: true, segment.StateCorrupt: true},
		segment.StateSealed: {segment.StatePruned: true, segment.StateCorrupt: true},
	}
	for _, from := range allStates {
		for _, to := range allStates {
			if valid[from][to] {
				continue
			}
			t.Run(from.String()+" to "+to.String(), func(t *testing.T) {
				require.False(t, from.CanTransitionTo(to))
				_, err := segment.Transition(from, to)
				require.ErrorIs(t, err, segment.ErrInvalidTransition)
				require.Contains(t, err.Error(), from.String())
				require.Contains(t, err.Error(), to.String())
			})
		}
	}
}

// TestStateMachine_UnknownStatesRejected covers the default arm of the
// transition switch from both sides.
func TestStateMachine_UnknownStatesRejected(t *testing.T) {
	tests := []struct {
		name string
		from segment.State
		to   segment.State
	}{
		{"unknown source", segment.State(0), segment.StateSealed},
		{"unknown target", segment.StateActive, segment.State(99)},
		{"both unknown", segment.State(42), segment.State(43)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.False(t, tt.from.CanTransitionTo(tt.to))
			_, err := segment.Transition(tt.from, tt.to)
			require.ErrorIs(t, err, segment.ErrInvalidTransition)
		})
	}
}

// TestState_Terminal marks the states no further transition leaves.
func TestState_Terminal(t *testing.T) {
	require.False(t, segment.StateActive.Terminal())
	require.False(t, segment.StateSealed.Terminal())
	require.True(t, segment.StatePruned.Terminal())
	require.True(t, segment.StateCorrupt.Terminal())
	require.False(t, segment.State(0).Terminal())
}

// TestErrInvalidTransition_IsNotACorruptionVerdict keeps the lifecycle
// programming error distinct from the on-medium verdicts: callers
// dispatching on ErrSegmentCorrupt must never catch a caller bug.
func TestErrInvalidTransition_IsNotACorruptionVerdict(t *testing.T) {
	_, err := segment.Transition(segment.StatePruned, segment.StateActive)
	require.Error(t, err)
	require.False(t, errors.Is(err, segment.ErrSegmentCorrupt))
	require.False(t, errors.Is(err, segment.ErrGap))
}
