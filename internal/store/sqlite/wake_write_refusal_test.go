// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that an ignored SQL write is never reported as a durable claim or outcome.
package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestClaimWake_IgnoredPersist_RefusesFence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reclaim bool
		trigger string
	}{
		{"new claim", false, "CREATE TRIGGER refuse_wake BEFORE INSERT ON wake_ledger BEGIN SELECT RAISE(IGNORE); END"},
		{"reclaim", true, "CREATE TRIGGER refuse_wake BEFORE UPDATE ON wake_ledger BEGIN SELECT RAISE(IGNORE); END"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, now := wakeLedgerStore(t)
			if tc.reclaim {
				_, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
				require.NoError(t, err)
				require.True(t, won)
				*now = now.Add(time.Minute)
			}
			// The allowlisted, constant trigger simulates a silently refused storage write.
			_, err := s.WriteDB().Exec(tc.trigger)
			require.NoError(t, err)
			claim, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
			require.ErrorIs(t, err, model.ErrConflict)
			require.False(t, won)
			require.Empty(t, claim.Token)
		})
	}
}

func TestRecordWakeOutcome_IgnoredPersist_ReturnsConflict(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	claim, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = s.WriteDB().Exec("CREATE TRIGGER refuse_outcome BEFORE UPDATE ON wake_ledger BEGIN SELECT RAISE(IGNORE); END")
	require.NoError(t, err)
	require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), claim, model.WakeDelivered), model.ErrConflict)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1, Claimed: 1}, stats)
}
