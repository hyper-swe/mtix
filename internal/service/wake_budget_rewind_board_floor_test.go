// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// TestWakeBudget_RewindIsDetectedAgainstBoardWideFloor pins that a snapshot
// behind the shared board floor is reported as a rewind even when the path it
// is observed at has never held that floor (MTIX-112.17.10.2 F11).
func TestWakeBudget_RewindIsDetectedAgainstBoardWideFloor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		operations []string
	}{
		{"older snapshot restored at a new path", []string{"wake", "snapshot:s", "wake", "wake", "move:B", "restore:s", "wake"}},
		{"copy lagging the shared floor", []string{"wake", "copy:B", "wake", "wake", "switch:B", "wake"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWakeBackoffFixture(t)
			limit := "12"
			require.NoError(t, f.state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
			sequence := newBudgetSequence(t, f)
			for _, operation := range tc.operations {
				sequence.step(operation)
				sequence.assertAccounting()
			}
			b := sequence.boards[sequence.active]
			m, present, err := f.state.ReadWakeBudget(b.board.keys[0].Recipient())
			require.NoError(t, err)
			require.True(t, present)
			require.Positive(t, m.Rewinds, "a counter behind the board-wide floor must record a rewind")
			require.False(t, m.LastRewindAt.IsZero())
			rows, err := f.alerts.Recent(t.Context(), 0)
			require.NoError(t, err)
			classes := make([]string, 0, len(rows))
			for _, row := range rows {
				classes = append(classes, row.Class)
			}
			require.Contains(t, classes, "budget.rewind")
		})
	}
}
