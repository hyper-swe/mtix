// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStatusRoutes_BlockedAndInvalidated_HaveNoRouteAndSayWhy (D5): no
// sequence of commands sends a blocked or invalidated status, so neither state
// may be given steps; each is printed with its reason instead of a route.
func TestStatusRoutes_BlockedAndInvalidated_HaveNoRouteAndSayWhy(t *testing.T) {
	want := map[string]string{
		"blocked":     "blocked: no route, change its dependencies",
		"invalidated": "invalidated: no route, it stays on this machine",
	}
	seen := 0
	for _, r := range statusRoutes() {
		reason, ok := want[r.state]
		if !ok {
			continue
		}
		seen++
		require.Empty(t, r.steps, "%s has no route", r.state)
		require.Contains(t, statusRouteText(), reason)
	}
	require.Equal(t, len(want), seen, "both states are in the table")
}

// TestStatusRoutes_EveryOtherState_HasAtLeastOneStep: only blocked and
// invalidated lack a route, so a state whose steps were dropped is caught.
func TestStatusRoutes_EveryOtherState_HasAtLeastOneStep(t *testing.T) {
	for _, r := range statusRoutes() {
		if r.state == "blocked" || r.state == "invalidated" {
			continue
		}
		require.NotEmpty(t, r.steps, "%s has a route", r.state)
	}
}
