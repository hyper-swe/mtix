// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWakeBudget_NewAckOnMovedBoardDoesNotRecharge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ack   bool
		prior int
	}{
		{"acknowledged", true, 6}, {"unacknowledged", false, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWakeBackoffFixture(t)
			limit := "12"
			require.NoError(t, f.state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
			b := f.board(t, 13)
			require.NoError(t, b.dispatcher(t, tc.ack, nil).Pass(t.Context(), b.keys[:tc.prior]))
			moveWakeBudgetBoard(t, b)
			// Handle a different pending event before this board's first new wake.
			require.NoError(t, service.NewInboxService(b.store).InboxAck(t.Context(), "budget-worker", b.keys[12].EventSeq))
			require.NoError(t, b.dispatcher(t, tc.ack, nil).Pass(t.Context(), b.keys[tc.prior:tc.prior+1]))
			require.Equal(t, int64(tc.prior+1), f.sent.Load(), "ACK publication must not turn an unseen counter into new wake debt")
			mirror, _, err := f.state.ReadWakeBudget(b.keys[0].Recipient())
			require.NoError(t, err)
			var used int64
			for _, credit := range mirror.Credits {
				used += credit.Count
			}
			require.Equal(t, int64(tc.prior+1), used)
			if tc.ack {
				require.Zero(t, mirror.Unacked)
			} else {
				require.Equal(t, int64(1), mirror.Unacked, "the ACK on an unseen board still resets the host recipient")
				require.True(t, mirror.NextWakeAt.IsZero())
			}
		})
	}
}
