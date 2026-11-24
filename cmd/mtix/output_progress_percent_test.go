// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// MTIX-294 (F6): a progress value below 1.0, such as the weighted clamp, never
// renders as 100% on a CLI surface (rows and bars).
func TestProgressRendering_BelowOne_NeverShows100Percent(t *testing.T) {
	below := math.Nextafter(1.0, 0.0)
	for _, p := range []float64{below, 0.996, 0.9951} {
		row := FormatNodeRow("PROJ-1", "in_progress", 2, "t", p, false)
		assert.Equal(t, "99%", row[3], "row for %v", p)
		assert.Contains(t, ProgressBar(p, 10), "] 99%", "bar for %v", p)
	}
	assert.Equal(t, "100%", FormatNodeRow("PROJ-1", "done", 2, "t", 1.0, false)[3])
	assert.Equal(t, "50%", FormatNodeRow("PROJ-1", "open", 2, "t", 0.5, false)[3])
	assert.Equal(t, "0%", FormatNodeRow("PROJ-1", "open", 2, "t", 0, false)[3])
}
