// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func checkpointFailureBudgets(t *testing.T) (*wakeBudgets, model.WakeKey, string) {
	t.Helper()
	state := testutil.OperatorState(t)
	limit := "2"
	require.NoError(t, state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	store, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	host, err := state.HostID()
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "checkpoint-worker", HostID: host, UID: "checkpoint-uid", EventSeq: 1, Epoch: 1}
	b := &wakeBudgets{store: store, state: state, settings: state, alerts: NewAlerts(state, func() time.Time { return now }), held: func(string) {}, clock: func() time.Time { return now }}
	for seq := int64(1); seq <= 2; seq++ {
		key.EventSeq = seq
		_, won, err := b.claim(t.Context(), key)
		require.NoError(t, err)
		require.True(t, won)
	}
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE wake_budget_counters SET total=1")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return b, key, path
}

func TestWakeBudget_RewindCheckpointFailureAbortsBeforeFloorHealing(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  int64
	}{{"held", 3}, {"nonclaimable", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			b, key, path := checkpointFailureBudgets(t)
			key.EventSeq = tc.seq
			policy, err := b.policy(key.AgentID)
			require.NoError(t, err)
			cause := errors.New("protected checkpoint storage failed")
			err = b.state.UpdateWakeBudget(t.Context(), key.Recipient(), func(m *oplocal.WakeBudgetMirror, _ func() error) error {
				a := wakeAdmission{key: key, policy: policy, mirror: m, save: func() error { return cause }}
				return b.claimLocked(t.Context(), &a)
			})
			require.ErrorIs(t, err, cause)
			require.Equal(t, int64(1), checkpointCounter(t, path), "a failed diagnostic checkpoint must abort before SQL floor healing")
			mirror, _, err := b.state.ReadWakeBudget(key.Recipient())
			require.NoError(t, err)
			require.Zero(t, mirror.Rewinds, "the failed checkpoint did not persist diagnostic evidence")
			_, won, err := b.claim(t.Context(), key)
			require.NoError(t, err)
			require.False(t, won)
			require.Equal(t, int64(2), checkpointCounter(t, path))
			mirror, _, err = b.state.ReadWakeBudget(key.Recipient())
			require.NoError(t, err)
			require.Positive(t, mirror.Rewinds, "retry must preserve the diagnostic before healing")
		})
	}
}

func checkpointCounter(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var total int64
	require.NoError(t, db.QueryRow("SELECT total FROM wake_budget_counters").Scan(&total))
	return total
}
