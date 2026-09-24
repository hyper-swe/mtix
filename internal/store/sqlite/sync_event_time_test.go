// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEventTime_WallClockRange_EventTimeOrApplyTime pins the one helper that
// turns an event's wall_clock_ts into a stored time (MTIX-95.26): within
// years 1..9999 it returns the event's own instant, in UTC and to the
// millisecond; outside that range it returns the apply time, in UTC.
func TestEventTime_WallClockRange_EventTimeOrApplyTime(t *testing.T) {
	applyTime := time.Date(2026, 9, 24, 1, 2, 3, 4, time.FixedZone("apply", 3600))
	firstOfYear1 := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	lastOf9999 := time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	firstOf10000 := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		ms   int64
		want time.Time
	}{
		{"in range keeps milliseconds, in UTC",
			time.Date(2026, 9, 1, 12, 0, 59, 999_000_000, time.FixedZone("x", 3600)).UnixMilli(),
			time.Date(2026, 9, 1, 11, 0, 59, 999_000_000, time.UTC)},
		{"Unix epoch", 0, time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"negative, before the epoch", -1, time.Date(1969, 12, 31, 23, 59, 59, 999_000_000, time.UTC)},
		{"first millisecond of year 1", firstOfYear1.UnixMilli(), firstOfYear1},
		{"year 0 is the apply time", firstOfYear1.UnixMilli() - 1, applyTime},
		{"last millisecond of year 9999", lastOf9999.UnixMilli(), lastOf9999},
		{"year 10000 is the apply time", firstOf10000.UnixMilli(), applyTime},
		{"largest wall_clock_ts is the apply time", math.MaxInt64, applyTime},
		{"smallest wall_clock_ts is the apply time", math.MinInt64, applyTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventTime(tt.ms, applyTime)
			require.True(t, tt.want.Equal(got), "eventTime(%d) = %s, want %s", tt.ms, got, tt.want)
			require.Equal(t, time.UTC, got.Location(), "stored times are UTC")
		})
	}
}
