// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-6.1: parseChangedSince accepts an RFC3339 timestamp OR a relative Go
// duration ("1h", "30m") meaning "since now minus that", and rejects garbage.

func TestParseChangedSince_Empty_ReturnsZero(t *testing.T) {
	ts, err := parseChangedSince("")
	require.NoError(t, err)
	assert.True(t, ts.IsZero(), "empty means no filter")
}

func TestParseChangedSince_RFC3339(t *testing.T) {
	ts, err := parseChangedSince("2026-04-06T10:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC), ts.UTC())
}

func TestParseChangedSince_BareDate(t *testing.T) {
	ts, err := parseChangedSince("2026-04-06")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC), ts.UTC())
}

func TestParseChangedSince_RelativeDuration(t *testing.T) {
	before := time.Now()
	ts, err := parseChangedSince("1h")
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(-time.Hour), ts, 5*time.Second,
		"a duration is interpreted as now minus that window")
}

func TestParseChangedSince_Rejects(t *testing.T) {
	for _, bad := range []string{"notatime", "-1h", "0s", "60"} {
		_, err := parseChangedSince(bad)
		require.Error(t, err, "must reject %q", bad)
	}
}
