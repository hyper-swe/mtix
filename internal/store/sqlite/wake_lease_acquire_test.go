// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// These tests verify ownership, renewal and persisted contention between store handles.

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func wakeKey() sqlite.WakeLeaseKey {
	return sqlite.WakeLeaseKey{AgentID: "TEST-agent", HostID: "TEST-host", UID: "1000"}
}

func wakeTime() time.Time { return time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) }

func wakeStore(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	return openWakeStore(t, path, wakeTime()), path
}

func openWakeStore(t *testing.T, path string, now time.Time) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	s.SetClock(func() time.Time { return now })
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestAcquireWakeLease_ConcurrentAcquire_OneWins(t *testing.T) {
	s, path := wakeStore(t)
	other := openWakeStore(t, path, wakeTime())
	start := make(chan struct{})
	errors := make(chan error, 2)
	leases := make(chan sqlite.WakeLease, 2)
	var wg sync.WaitGroup
	for _, handle := range []*sqlite.Store{s, other} {
		wg.Add(1)
		go func(store *sqlite.Store) {
			defer wg.Done()
			<-start
			lease, err := store.AcquireWakeLease(context.Background(), wakeKey(), "", 15*time.Minute)
			errors <- err
			leases <- lease
		}(handle)
	}
	close(start)
	wg.Wait()
	close(errors)
	wins, held := 0, 0
	for err := range errors {
		if err == nil {
			wins++
		} else {
			require.ErrorIs(t, err, model.ErrAlreadyClaimed)
			held++
		}
	}
	assert.Equal(t, 1, wins)
	assert.Equal(t, 1, held)
	var dirty int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT dirty FROM wake_leases WHERE agent_id=? AND host_id=? AND uid=?", "TEST-agent", "TEST-host", "1000").Scan(&dirty))
	assert.Equal(t, 1, dirty)
}

func TestAcquireWakeLease_HeldByOther_SetsDirty(t *testing.T) {
	s, path := wakeJournalStore(t)
	first, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", 15*time.Minute)
	require.NoError(t, err)
	assert.Regexp(t, "^[0-9a-f]{32}$", first.Holder)
	other := openWakeStore(t, path, wakeTime())
	held, err := other.AcquireWakeLease(t.Context(), wakeKey(), "", 15*time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Greater(t, first.HighWater, int64(0))
	assert.Empty(t, held.Holder)
	assert.Zero(t, held.HighWater)
	assert.Equal(t, first.ExpiresAt, held.ExpiresAt)
	var storedHolder string
	var storedMark int64
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT holder,high_water FROM wake_leases").Scan(&storedHolder, &storedMark))
	assert.Equal(t, first.Holder, storedHolder)
	assert.Equal(t, first.HighWater, storedMark)
	third := openWakeStore(t, path, wakeTime())
	_, err = third.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	var dirty int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT dirty FROM wake_leases").Scan(&dirty))
	assert.Equal(t, 1, dirty)
}

func TestAcquireWakeLease_ExpiredRow_TakenOverWithNewHolder(t *testing.T) {
	for _, delay := range []time.Duration{time.Minute, time.Minute + time.Nanosecond} {
		t.Run(delay.String(), func(t *testing.T) {
			s, path := wakeStore(t)
			first, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
			require.NoError(t, err)
			later := openWakeStore(t, path, wakeTime().Add(delay))
			second, err := later.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
			require.NoError(t, err)
			assert.NotEqual(t, first.Holder, second.Holder)
			assert.Equal(t, wakeTime().Add(delay), second.AcquiredAt)
			var dirty int
			require.NoError(t, newTestDB(t, path).QueryRow("SELECT dirty FROM wake_leases").Scan(&dirty))
			assert.Zero(t, dirty)
		})
	}
}

func TestAcquireWakeLease_RenewWithOwnHolder_ExtendsExpiry(t *testing.T) {
	for _, delay := range []time.Duration{30 * time.Second, 2 * time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			s, path := wakeStore(t)
			first, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
			require.NoError(t, err)
			_, err = s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
			require.ErrorIs(t, err, model.ErrAlreadyClaimed)
			later := openWakeStore(t, path, wakeTime().Add(delay))
			second, err := later.AcquireWakeLease(t.Context(), wakeKey(), first.Holder, 15*time.Minute)
			require.NoError(t, err)
			assert.Equal(t, first.Holder, second.Holder)
			assert.Equal(t, first.AcquiredAt, second.AcquiredAt)
			assert.Equal(t, first.HighWater, second.HighWater)
			assert.Equal(t, wakeTime().Add(delay+15*time.Minute), second.ExpiresAt)
			var dirty int
			require.NoError(t, newTestDB(t, path).QueryRow("SELECT dirty FROM wake_leases").Scan(&dirty))
			assert.Equal(t, 1, dirty)
		})
	}
}

func TestAcquireWakeLease_ForeignHostOrUID_Independent(t *testing.T) {
	s, path := wakeStore(t)
	keys := []sqlite.WakeLeaseKey{wakeKey(), {AgentID: "TEST-agent", HostID: "TEST-other", UID: "1000"}, {AgentID: "TEST-agent", HostID: "TEST-host", UID: "1001"}, {AgentID: "TEST-other", HostID: "TEST-host", UID: "1000"}}
	for _, key := range keys {
		_, err := s.AcquireWakeLease(t.Context(), key, "", time.Minute)
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT count(*) FROM wake_leases WHERE dirty=0").Scan(&count))
	assert.Equal(t, len(keys), count)
}

func TestAcquireWakeLease_InvalidInput_ReturnsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		key    sqlite.WakeLeaseKey
		holder string
		ttl    time.Duration
	}{
		{"EmptyAgent", sqlite.WakeLeaseKey{HostID: "TEST-host", UID: "1000"}, "", time.Minute},
		{"EmptyHost", sqlite.WakeLeaseKey{AgentID: "TEST-agent", UID: "1000"}, "", time.Minute},
		{"EmptyUID", sqlite.WakeLeaseKey{AgentID: "TEST-agent", HostID: "TEST-host"}, "", time.Minute},
		{"ShortHolder", wakeKey(), "abcd", time.Minute},
		{"UpperHolder", wakeKey(), "ABCDEF0123456789ABCDEF0123456789", time.Minute},
		{"ShortTTL", wakeKey(), "", time.Minute - time.Nanosecond},
		{"LongTTL", wakeKey(), "", 24*time.Hour + time.Nanosecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := wakeStore(t)
			_, err := s.AcquireWakeLease(t.Context(), tc.key, tc.holder, tc.ttl)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}
