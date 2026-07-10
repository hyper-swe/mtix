// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-51 / FR-19.5: the durable wake path for a harness-hosted (Claude) agent
// is the mtix_inbox_wait MCP tool, NOT a backgrounded CLI `inbox --wait` park
// (the harness reaps long-lived child processes). For that re-invoke loop to
// work the single tool call MUST be bounded, so control returns to the agent on
// an empty timeout and it re-invokes to keep parking. These tests pin that cap.
package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClampInboxWaitSeconds: a requested wait is bounded to the ceiling.
// Zero/negative (unset or nonsensical) and over-cap requests collapse to the
// cap; an in-range request passes through unchanged.
func TestClampInboxWaitSeconds(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero unset -> cap", 0, maxInboxWaitSeconds},
		{"negative -> cap", -1, maxInboxWaitSeconds},
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
