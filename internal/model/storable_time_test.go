// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestIsStorableTime_UTCYearBoundaries_OnlyYears1To9999 verifies the stored
// timestamp range is decided on the UTC year, so offset forms that cross a
// year boundary are judged by the year they are stored in (MTIX-95.22).
func TestIsStorableTime_UTCYearBoundaries_OnlyYears1To9999(t *testing.T) {
	east := time.FixedZone("UTC+1", 60*60)
	west := time.FixedZone("UTC-5", -5*60*60)
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"first second of year 1", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"last second of year 9999", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"year 0", time.Date(0, 12, 31, 23, 59, 59, 0, time.UTC), false},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"year 1 east of utc is year 0 in utc", time.Date(1, 1, 1, 0, 59, 59, 0, east), false},
		{"year 9999 west of utc is year 10000 in utc", time.Date(9999, 12, 31, 19, 0, 0, 0, west), false},
		{"year 9999 west of utc still in 9999", time.Date(9999, 12, 31, 18, 59, 59, 0, west), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.IsStorableTime(tt.t))
		})
	}
}

// TestNodeValidate_DeferUntilOutsideStorableYears_ReturnsInvalidInput
// verifies node validation refuses an unreadable wake time (MTIX-95.22).
func TestNodeValidate_DeferUntilOutsideStorableYears_ReturnsInvalidInput(t *testing.T) {
	bad := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	good := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	n := &model.Node{Title: "t", DeferUntil: &bad}
	assert.True(t, errors.Is(n.Validate(), model.ErrInvalidInput))
	n.DeferUntil = &good
	assert.NoError(t, n.Validate())
}
