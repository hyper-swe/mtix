// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// FR-19: explicit zero requests an unbounded wait; positive requests retain
// the tool ceiling for bounded foreground calls. Omission is tested at the handler.
package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClampInboxWaitSeconds: a requested wait is bounded to the ceiling.
// Explicit zero passes through; over-cap positive requests collapse to the
// cap; an in-range positive request passes through unchanged.
func TestClampInboxWaitSeconds(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"explicit zero -> unbounded", 0, 0},
		{"one over cap -> cap", maxInboxWaitSeconds + 1, maxInboxWaitSeconds},
		{"far over cap -> cap", 100000, maxInboxWaitSeconds},
		{"one second passes through", 1, 1},
		{"mid-range passes through", 30, 30},
		{"exactly the cap passes through", maxInboxWaitSeconds, maxInboxWaitSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, clampInboxWaitSeconds(tc.in))
		})
	}
}

// TestInboxWaitCap_IsSixtySeconds pins the ceiling value — load-bearing for the
// "re-invoke on empty" agent loop, and quoted in the tool description + docs.
func TestInboxWaitCap_IsSixtySeconds(t *testing.T) {
	assert.Equal(t, 60, maxInboxWaitSeconds)
}
