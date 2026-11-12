// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the store half of stalled-turn recovery (MTIX-112.40.12, FR-20): the one
// compare-and-set reopen of a sent wake whose confirmation never came, its retry cap of
// one, and the live-waiter read the stall rule needs.
package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func stallKey(seq int64) model.WakeKey {
	return model.WakeKey{AgentID: "TEST-agent", HostID: "TEST-host", UID: "1000", EventSeq: seq, Epoch: 1}
}

// sentRow claims an event and records it sent at the store's current clock.
func sentRow(t *testing.T, s *sqlite.Store, key model.WakeKey) {
	t.Helper()
	attempt, won, err := s.ClaimWake(context.Background(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeResult(context.Background(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}))
}

func TestReopenStalledWake_SentPastWindow_ReopensOnceThenCaps(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/a.db", now)
	key := stallKey(1)
	sentRow(t, s, key)
	later := now.Add(time.Minute)
	s.SetClock(func() time.Time { return later })

	attempt, ok, err := s.ReopenStalledWake(context.Background(), key, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok, "a sent row older than the window is reopened")
	require.True(t, attempt.Reclaimed)
	require.NotEmpty(t, attempt.Token)
	got, found, err := s.ReadWakeResult(context.Background(), key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, model.WakeClaimed, got.Outcome)

	// The reopened claim is finished (sent again); a second stall hits the cap of one.
	require.NoError(t, s.RecordWakeResult(context.Background(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}))
	later2 := later.Add(time.Minute)
	s.SetClock(func() time.Time { return later2 })
	_, ok, err = s.ReopenStalledWake(context.Background(), key, 30*time.Second)
	require.NoError(t, err)
	require.False(t, ok, "the retry cap is one")
	got, _, err = s.ReadWakeResult(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeUndeliverable, got.Outcome)
	require.Equal(t, model.WakeReasonStallUnconfirmed, got.Reason)
}

func TestReopenStalledWake_NotStalledRows_AreUntouched(t *testing.T) {
	now := wakeTime()
	window := 30 * time.Second
	tests := []struct {
		name  string
		setup func(t *testing.T, s *sqlite.Store, key model.WakeKey)
		want  model.WakeOutcome
	}{
		{"sent within the window", func(t *testing.T, s *sqlite.Store, key model.WakeKey) { sentRow(t, s, key) }, model.WakeSent},
		{"confirmed", func(t *testing.T, s *sqlite.Store, key model.WakeKey) {
			sentRow(t, s, key)
			require.NoError(t, s.UpgradeWakeConfirmed(context.Background(), key))
		}, model.WakeConfirmed},
		{"in-flight claim", func(t *testing.T, s *sqlite.Store, key model.WakeKey) {
			_, won, err := s.ClaimWake(context.Background(), key, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
		}, model.WakeClaimed},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openWakeStore(t, t.TempDir()+"/b.db", now)
			key := stallKey(int64(i + 1))
			tt.setup(t, s, key)
			_, ok, err := s.ReopenStalledWake(context.Background(), key, window)
			require.NoError(t, err)
			require.False(t, ok)
			got, _, err := s.ReadWakeResult(context.Background(), key)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Outcome)
		})
	}
	t.Run("no row", func(t *testing.T) {
		s := openWakeStore(t, t.TempDir()+"/c.db", now)
		_, ok, err := s.ReopenStalledWake(context.Background(), stallKey(9), window)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("non-positive window", func(t *testing.T) {
		s := openWakeStore(t, t.TempDir()+"/g.db", now)
		_, _, err := s.ReopenStalledWake(context.Background(), stallKey(1), 0)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	})
	t.Run("invalid key", func(t *testing.T) {
		s := openWakeStore(t, t.TempDir()+"/d.db", now)
		_, _, err := s.ReopenStalledWake(context.Background(), model.WakeKey{}, window)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	})
}

func TestReopenStalledWake_ConcurrentReopeners_ExactlyOneWins(t *testing.T) {
	now := wakeTime()
	dir := t.TempDir()
	first := openWakeStore(t, dir+"/e.db", now)
	key := stallKey(1)
	sentRow(t, first, key)
	later := now.Add(time.Minute)
	first.SetClock(func() time.Time { return later })
	second := openWakeStore(t, dir+"/e.db", later)
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(s *sqlite.Store) {
			defer wg.Done()
			_, ok, err := s.ReopenStalledWake(context.Background(), key, 30*time.Second)
			require.NoError(t, err)
			wins <- ok
		}([]*sqlite.Store{first, second}[i%2])
	}
	wg.Wait()
	close(wins)
	won := 0
	for ok := range wins {
		if ok {
			won++
		}
	}
	require.Equal(t, 1, won)
}

func TestWakeWaiterLive_FollowsLeaseExpiry(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/f.db", now)
	live, err := s.WakeWaiterLive(context.Background(), wakeKey())
	require.NoError(t, err)
	require.False(t, live, "no lease row: nobody is waiting")
	_, err = s.AcquireWakeLease(context.Background(), wakeKey(), "", 15*time.Minute)
	require.NoError(t, err)
	live, err = s.WakeWaiterLive(context.Background(), wakeKey())
	require.NoError(t, err)
	require.True(t, live)
	s.SetClock(func() time.Time { return now.Add(16 * time.Minute) })
	live, err = s.WakeWaiterLive(context.Background(), wakeKey())
	require.NoError(t, err)
	require.False(t, live, "an expired lease is no waiter")
	_, err = s.WakeWaiterLive(context.Background(), sqlite.WakeLeaseKey{})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
