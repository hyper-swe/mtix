// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSupportedRange_CurrentProtocol_LowerBoundNeverBelowOne(t *testing.T) {
	tests := []struct {
		name           string
		current        int
		wantLo, wantHi int
	}{
		{"first protocol has no N-1", 1, 1, 1},
		{"second protocol serves N-1", 2, 1, 2},
		{"later protocol serves N-1", 7, 6, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lo, hi := SupportedRange(tt.current)
			assert.Equal(t, tt.wantLo, lo)
			assert.Equal(t, tt.wantHi, hi)
		})
	}
}

func TestNegotiate_RequestedProtocol_ServesRefreshesOrRefuses(t *testing.T) {
	tests := []struct {
		name               string
		current, requested int
		want               Decision
	}{
		{"current is served", 2, 2, DecisionServe},
		{"N-1 is served and refreshed", 2, 1, DecisionServeAndRefresh},
		{"N-2 is out of range", 3, 1, DecisionOutOfRange},
		{"newer glue is out of range", 2, 3, DecisionOutOfRange},
		{"protocol zero never existed", 1, 0, DecisionOutOfRange},
		{"protocol zero is out of range at 2", 2, 0, DecisionOutOfRange},
		{"negative is out of range", 1, -1, DecisionOutOfRange},
		{"production protocol is served", Protocol, Protocol, DecisionServe},
		{"invalid current refuses everything", 0, 0, DecisionOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Negotiate(tt.current, tt.requested))
		})
	}
}

func TestDecisionString_EachDecision_Named(t *testing.T) {
	assert.Equal(t, "serve", DecisionServe.String())
	assert.Equal(t, "serve-and-refresh", DecisionServeAndRefresh.String())
	assert.Equal(t, "out-of-range", DecisionOutOfRange.String())
	assert.Equal(t, "unknown", Decision(0).String())
}

func TestKnownHarnessAndVerb_Names_MatchADR(t *testing.T) {
	assert.Equal(t, []string{"claude-code", "codex", "pi"}, Harnesses())
	assert.Equal(t, []string{"session-start", "prompt-submit", "session-end", "wait"}, Verbs())
	assert.True(t, KnownHarness("codex"))
	assert.False(t, KnownHarness("Codex"))
	assert.False(t, KnownHarness(""))
}
