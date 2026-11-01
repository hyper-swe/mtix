// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestWakeBudget_PathSequencesChargeEachFencedAttemptOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		operations []string
	}{
		{"move back acknowledged", []string{"wake", "ack", "wake", "ack", "move:B", "wake", "ack", "wake", "ack", "wake", "ack", "move:A", "wake"}},
		{"move back unacknowledged", []string{"wake", "move:B", "wake", "wake", "move:A", "wake", "wake", "wake"}},
		{"three paths", []string{"wake", "move:B", "wake", "move:C", "wake", "move:A", "wake", "move:B", "wake", "wake"}},
		{"ack then move", []string{"wake", "wake", "ack", "move:B", "wake", "move:A", "wake"}},
		{"move then ack", []string{"wake", "wake", "move:B", "ack", "wake", "move:A", "wake"}},
		{"ack future before dispatch", []string{"wake", "move:B", "wake", "move:A", "ack-future", "wake", "ack-repeat", "wake"}},
		{"restore same path", []string{"wake", "snapshot:s", "wake", "wake", "restore:s", "wake", "duplicate"}},
		{"restore new path", []string{"wake", "snapshot:s", "wake", "move:B", "wake", "restore:s", "wake", "move:A", "wake"}},
		{"restore before identity", []string{"snapshot:s", "wake", "move:B", "wake", "restore:s", "wake", "move:A", "wake"}},
		{"two copies interleaved", []string{"wake", "copy:B", "wake", "switch:B", "wake", "switch:A", "wake", "switch:B", "wake", "wake", "ack", "switch:A", "wake"}},
		{"separate board no refund", []string{"wake", "move:B", "wake", "new:D", "wake", "ack", "switch:B", "wake", "move:A", "wake"}},
		{"send error and duplicate", []string{"error", "duplicate", "move:B", "error", "move:A", "wake", "duplicate", "ack", "wake"}},
		{"hourly cap across moves", budgetCapMoveSequence()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWakeBackoffFixture(t)
			limit := "12"
			require.NoError(t, f.state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
			sequence := newBudgetSequence(t, f)
			for i, operation := range tc.operations {
				t.Logf("step %d: %s", i, operation)
				sequence.step(operation)
				sequence.assertAccounting()
			}
		})
	}
}

func budgetCapMoveSequence() []string {
	ops := []string{}
	for range 7 {
		ops = append(ops, "wake", "ack", "move:B", "wake", "ack", "move:A")
	}
	return ops
}
