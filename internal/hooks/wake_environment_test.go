// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWakeExecEnvironment_MaximumCoveredHopAndAuditAttempt(t *testing.T) {
	wake := WakeContextForAttempt(model.WakeAttempt{Token: "fenced-attempt", Hop: 31})
	wake.Hops = append(wake.Hops, 3, 8)
	values, err := wakeExecEnvironment(wake)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"MTIX_WAKE_HOP=31", "MTIX_WAKE_ATTEMPT=fenced-attempt"}, values)
	for _, tt := range []struct {
		name string
		wake *WakeExecContext
	}{
		{"empty attempt", &WakeExecContext{Hops: []int64{1}}},
		{"empty coverage", &WakeExecContext{Attempt: "attempt"}},
		{"negative hop", &WakeExecContext{Attempt: "attempt", Hops: []int64{-1}}},
		{"invalid audit token", &WakeExecContext{Attempt: "bad\nvalue", Hops: []int64{1}}},
	} {
		t.Run(tt.name, func(t *testing.T) { _, err := wakeExecEnvironment(tt.wake); require.Error(t, err) })
	}
	values, err = wakeExecEnvironment(nil)
	require.NoError(t, err)
	require.Empty(t, values)
}
