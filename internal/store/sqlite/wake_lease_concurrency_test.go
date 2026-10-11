// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Concurrent release and contention must retain one owner's handoff token.
package sqlite_test

import (
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseWakeLease_ConcurrentContender_RetainsOwner(t *testing.T) {
	s, path := wakeJournalStore(t)
	lease := requireWakeLease(t, s)
	wakeEvent(t, s, path, "event", wakeKey().AgentID)
	other := openWakeStore(t, path, wakeTime())
	start := make(chan struct{})
	release := make(chan sqlite.ReleaseResult, 1)
	errs := make(chan error, 2)
	go func() {
		<-start
		result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
		release <- result
		errs <- err
	}()
	go func() {
		<-start
		_, err := other.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
		errs <- err
	}()
	close(start)
	held, success := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, model.ErrAlreadyClaimed)
			held++
		}
	}
	assert.Equal(t, 1, held)
	assert.Equal(t, 1, success)
	assert.Equal(t, "relaunch", (<-release).Outcome)
	row, err := other.AcquireWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, lease.Holder, row.Holder)
}

func TestAcquireWakeLease_DirtyAfterClose_PersistsOnReopen(t *testing.T) {
	s, path := wakeStore(t)
	lease := requireWakeLease(t, s)
	_, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	require.NoError(t, s.Close())
	reopened := openWakeStore(t, path, wakeTime())
	var token string
	var dirty int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT holder,dirty FROM wake_leases").Scan(&token, &dirty))
	assert.Equal(t, lease.Holder, token)
	assert.Equal(t, 1, dirty)
	renewed, err := reopened.AcquireWakeLease(t.Context(), wakeKey(), token, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, token, renewed.Holder)
}

func TestAcquireWakeLease_FractionalFutureExpiry_RemainsHeld(t *testing.T) {
	s, path := wakeStore(t)
	s.SetClock(func() time.Time { return wakeTime().Add(-100 * time.Millisecond) })
	lease, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.NoError(t, err)
	s.SetClock(func() time.Time { return wakeTime().Add(59 * time.Second) })
	current, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Empty(t, current.Holder)
	assert.Zero(t, current.HighWater)
	assert.Equal(t, lease.ExpiresAt, current.ExpiresAt)
	var holder string
	var dirty int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT holder,dirty FROM wake_leases").Scan(&holder, &dirty))
	assert.Equal(t, lease.Holder, holder)
	assert.Equal(t, 1, dirty)
}
