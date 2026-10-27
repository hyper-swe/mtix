// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func moveWakeBudgetBoard(t *testing.T, b *wakeBudgetBoard) {
	t.Helper()
	require.NoError(t, b.store.Close())
	moved := filepath.Join(t.TempDir(), "moved.db")
	require.NoError(t, os.Rename(b.path, moved))
	b.path = moved
	b.open(t)
}

func TestWakeBudget_MovedBoardDoesNotRechargeHistoricalAttempts(t *testing.T) {
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
			require.NoError(t, b.dispatcher(t, tc.ack, nil).Pass(t.Context(), b.keys[tc.prior:tc.prior+1]))
			require.Equal(t, int64(tc.prior+1), f.sent.Load(), "a new source path must not replay the lifetime counter as new wakes")
			assertMovedWakeBudget(t, b, tc.ack, tc.prior+1)
		})
	}
}

func assertMovedWakeBudget(t *testing.T, b *wakeBudgetBoard, ack bool, count int) {
	t.Helper()
	f := b.fixture
	mirror, _, err := f.state.ReadWakeBudget(b.keys[0].Recipient())
	require.NoError(t, err)
	var used int64
	for _, credit := range mirror.Credits {
		used += credit.Count
	}
	require.Equal(t, int64(count), used)
	if !ack {
		require.Equal(t, int64(5), mirror.Unacked)
		require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[count:count+1]))
		require.Equal(t, int64(count), f.sent.Load(), "the next wake still waits after five real unacked attempts")
		return
	}
	require.Zero(t, mirror.Unacked)
	other := f.board(t, 6)
	require.NoError(t, other.dispatcher(t, true, nil).Pass(t.Context(), other.keys))
	require.Equal(t, int64(12), f.sent.Load(), "the move neither consumes extra credit nor refunds the earlier six wakes")
	require.Equal(t, int64(5), budgetLedgerRows(t, other))
}
