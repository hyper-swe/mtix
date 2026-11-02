// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestWakeInboxBudget_Limit_SharesRollingAllowance covers ADR-007 B1: with a
// configured budget of 12 the 13th return in an hour is refused, and the hour
// rolls.
func TestWakeInboxBudget_Limit_SharesRollingAllowance(t *testing.T) {
	f := newWakeBudgetFixture(t, "12")
	b := service.WakeInboxBudget{State: f.state, HostID: f.host, UID: f.uid, Clock: func() time.Time { return f.now }}
	for _, tt := range []struct {
		name string
		at   time.Duration
		want int64
		got  int64
	}{
		{"first five", 0, 5, 5},
		{"next five", time.Minute, 5, 5},
		{"only two left", 2 * time.Minute, 5, 2},
		{"exhausted", 3 * time.Minute, 1, 0},
		{"hour rolls", 61 * time.Minute, 5, 5},
	} {
		f.now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC).Add(tt.at)
		got, err := b.Admit(t.Context(), "w", make([]string, tt.want))
		require.NoError(t, err, tt.name)
		assert.Equal(t, tt.got, got, tt.name)
	}
	mirror, present, err := f.state.ReadWakeBudget(model.WakeRecipient{AgentID: "w", HostID: f.host, UID: f.uid})
	require.NoError(t, err)
	require.True(t, present)
	assert.Zero(t, mirror.Unacked, "inbox charges never touch the dispatcher's unacked counter")
}

func TestWakeInboxBudget_NoLimit_GrantsAllAndRecordsNothing(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	b := service.WakeInboxBudget{State: f.state, HostID: f.host, UID: f.uid, Clock: func() time.Time { return f.now }}
	got, err := b.Admit(t.Context(), "w", make([]string, 500))
	require.NoError(t, err)
	assert.Equal(t, int64(500), got)
	got, err = b.Admit(t.Context(), "w", nil)
	require.NoError(t, err)
	assert.Zero(t, got)
	_, present, err := f.state.ReadWakeBudget(model.WakeRecipient{AgentID: "w", HostID: f.host, UID: f.uid})
	require.NoError(t, err)
	assert.False(t, present)
}

func TestWakeInboxBudget_InvalidLimit_Refused(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	b := service.WakeInboxBudget{State: f.state, HostID: f.host, UID: f.uid, Clock: func() time.Time { return time.Time{} }}
	limit := "3"
	require.NoError(t, f.state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
	_, err := b.Admit(t.Context(), "w", []string{""})
	require.Error(t, err, "an invalid accounting clock is refused, never silently uncapped")
}
