// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests fenced claims, terminal outcomes and lease recovery through the real store.
package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func wakeLedgerStore(t *testing.T) (*sqlite.Store, *time.Time) {
	t.Helper()
	stores, now := wakeLedgerStores(t, 1)
	return stores[0], now
}

func wakeLedgerStores(t *testing.T, count int) ([]*sqlite.Store, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wake.db")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var stores []*sqlite.Store
	for range count {
		s, err := sqlite.New(path, slog.Default())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, s.Close()) })
		s.SetClock(func() time.Time { return now })
		stores = append(stores, s)
	}
	return stores, &now
}

func wakeLedgerKey() model.WakeKey {
	return model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: 7, Epoch: 1}
}

func TestClaimWake_ConcurrentClaimers_ExactlyOnce(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(map[int]string{1: "shared handle", 2: "independent handles"}[count], func(t *testing.T) {
			stores, _ := wakeLedgerStores(t, count)
			runWakeClaimRace(t, stores)
		})
	}
}

func runWakeClaimRace(t *testing.T, stores []*sqlite.Store) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan bool, 32)
	errors := make(chan error, 32)
	var group sync.WaitGroup
	for i := range 32 {
		group.Go(func() {
			<-start
			claim, won, err := stores[i%len(stores)].ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
			if won && claim.Token == "" {
				t.Error("winning claim has no attempt fence")
			}
			results <- won
			errors <- err
		})
	}
	close(start)
	group.Wait()
	winners := 0
	for range 32 {
		require.NoError(t, <-errors)
		if <-results {
			winners++
		}
	}
	require.Equal(t, 1, winners)
}

func TestStaleWakeClaims_CrashAfterClaim_ReclaimedAfterLease(t *testing.T) {
	s, now := wakeLedgerStore(t)
	first, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	*now = now.Add(time.Minute - time.Nanosecond)
	stale, err := s.StaleWakeClaims(t.Context(), time.Minute)
	require.NoError(t, err)
	require.Empty(t, stale)
	*now = now.Add(time.Nanosecond)
	stale, err = s.StaleWakeClaims(t.Context(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []model.WakeAttempt{first}, stale)
	second, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NotEqual(t, first.Token, second.Token)
	require.Equal(t, *now, second.ClaimedAt)
	require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), first, model.WakeDelivered), model.ErrConflict)
	require.NoError(t, s.RecordWakeOutcome(t.Context(), second, model.WakeError))
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1, Rejected: 1}, stats)
}

func TestRecordWakeOutcome_TerminalOutcomes_NeverReclaim(t *testing.T) {
	for _, outcome := range []model.WakeOutcome{model.WakeDelivered, model.WakeError} {
		t.Run(string(outcome), func(t *testing.T) {
			s, now := wakeLedgerStore(t)
			claim, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			require.NoError(t, s.RecordWakeOutcome(t.Context(), claim, outcome))
			require.NoError(t, s.RecordWakeOutcome(t.Context(), claim, outcome))
			*now = now.Add(24 * time.Hour)
			_, won, err = s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
			require.NoError(t, err)
			require.False(t, won)
			stale, err := s.StaleWakeClaims(t.Context(), time.Minute)
			require.NoError(t, err)
			require.Empty(t, stale)
		})
	}
}

func TestClaimWake_RegistrationKeyParts_IsolateClaims(t *testing.T) {
	for _, change := range []func(*model.WakeKey){
		func(k *model.WakeKey) { k.AgentID = "other-agent" },
		func(k *model.WakeKey) { k.HostID = "other-host" },
		func(k *model.WakeKey) { k.UID = "other-uid" },
		func(k *model.WakeKey) { k.EventSeq++ },
		func(k *model.WakeKey) { k.Epoch++ },
	} {
		s, _ := wakeLedgerStore(t)
		_, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
		require.NoError(t, err)
		require.True(t, won)
		other := wakeLedgerKey()
		change(&other)
		_, won, err = s.ClaimWake(t.Context(), other, time.Minute)
		require.NoError(t, err)
		require.True(t, won)
	}
}
