// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A list that silently returns no rows would read as an empty board
// (FR-6.3, FR-17.9, MTIX-269): non-positive limits are refused with the fix.
func TestRunList_NonPositiveLimit_ReturnsErrorNamingTheFix(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Counted", "", "", 3, "", "", "", "", ""))

	for _, limit := range []int{0, -1} {
		err := runList(listOptions{limit: limit})
		require.Error(t, err, "limit %d", limit)
		assert.Contains(t, err.Error(), "--limit must be at least 1")
		assert.Contains(t, err.Error(), "mtix stats")
		assert.Contains(t, err.Error(), "raise --limit")
	}
}

// Truncation must be visible to a human reader and say how to see more.
func TestRunList_TruncatedTable_SaysShowingNOfTotal(t *testing.T) {
	initTestApp(t)
	for i := 0; i < 3; i++ {
		require.NoError(t, runCreate("Paginated", "", "", 3, "", "", "", "", ""))
	}
	out := captureStdout(t, func() { require.NoError(t, runList(listOptions{limit: 1})) })
	assert.Contains(t, out, "showing 1 of 3")
	assert.Contains(t, out, "--limit")

	full := captureStdout(t, func() { require.NoError(t, runList(listOptions{limit: 50})) })
	assert.NotContains(t, full, "showing")
}

// The footer must appear when exactly one row is cut and must not when the
// rows equal the total (boundary of the truncation check, MTIX-269).
func TestRunList_FooterBoundary_ShowsOnlyWhenRowsAreCut(t *testing.T) {
	initTestApp(t)
	for i := 0; i < 2; i++ {
		require.NoError(t, runCreate("Boundary", "", "", 3, "", "", "", "", ""))
	}
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{"one row cut", 1, "showing 1 of 2"},
		{"rows equal total", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { require.NoError(t, runList(listOptions{limit: tt.limit})) })
			if tt.want == "" {
				assert.NotContains(t, out, "showing")
				return
			}
			assert.Contains(t, out, tt.want)
		})
	}
}
