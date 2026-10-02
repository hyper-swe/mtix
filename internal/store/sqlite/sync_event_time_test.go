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

// TestDeferWakeTime_UntilRange_UTCOrNoWakeTime pins the helper that turns a
// defer payload's until into defer_until (MTIX-95.26): the until in UTC when
// its UTC year lies within 1..9999, else nil (no wake time).
func TestDeferWakeTime_UntilRange_UTCOrNoWakeTime(t *testing.T) {
	minus5 := time.FixedZone("minus5", -5*3600)
	plus1 := time.FixedZone("plus1", 3600)
	at := func(v time.Time) *time.Time { return &v }
	firstOfYear1 := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	lastOf9999 := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	tests := []struct {
		name  string
		until *time.Time
		want  *time.Time // nil: no wake time
	}{
		{"no until", nil, nil},
		{"in range, in UTC", at(time.Date(2027, 1, 1, 9, 30, 0, 0, plus1)),
			at(time.Date(2027, 1, 1, 8, 30, 0, 0, time.UTC))},
		{"first second of year 1", at(firstOfYear1), at(firstOfYear1)},
		{"UTC year 0", at(time.Date(1, 1, 1, 0, 30, 0, 0, plus1)), nil},
		{"last second of year 9999", at(lastOf9999), at(lastOf9999)},
		{"UTC year 10000", at(time.Date(9999, 12, 31, 23, 0, 0, 0, minus5)), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deferWakeTime(tt.until)
			if tt.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.True(t, tt.want.Equal(*got), "deferWakeTime = %s, want %s", *got, *tt.want)
			require.Equal(t, time.UTC, got.Location(), "stored times are UTC")
		})
	}
}
