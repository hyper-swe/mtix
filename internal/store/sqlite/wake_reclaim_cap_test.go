// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that an expired unfinished wake claim is reclaimed at most once, marked
// reclaimed-after-crash, and then closed as undeliverable/reclaim-cap (MTIX-112.40.13).
package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type reclaimClaimer func(s *sqlite.Store, key model.WakeKey) (model.WakeAttempt, bool, error)

func reclaimClaimers(t *testing.T) map[string]reclaimClaimer {
	return map[string]reclaimClaimer{
		"ClaimWake": func(s *sqlite.Store, key model.WakeKey) (model.WakeAttempt, bool, error) {
			return s.ClaimWake(t.Context(), key, time.Minute)
		},
		"ClaimWakeBudget": func(s *sqlite.Store, key model.WakeKey) (model.WakeAttempt, bool, error) {
			r, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				return model.WakeBudgetDecision{Allow: true}, nil
			})
			return r.Attempt, r.Won, err
		},
	}
}

func reclaimRow(t *testing.T, s *sqlite.Store) (outcome, reason string) {
	t.Helper()
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), "SELECT outcome,reason FROM wake_ledger").Scan(&outcome, &reason))
	return outcome, reason
}

func TestClaimWake_ExpiredClaim_ReclaimsOnceThenCaps(t *testing.T) {
	for name, claim := range reclaimClaimers(t) {
		t.Run(name, func(t *testing.T) {
			s, err := sqlite.New(t.TempDir(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
			s.SetClock(func() time.Time { return now })
			key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 5, Epoch: 1}

			first, won, err := claim(s, key)
			require.NoError(t, err)
			require.True(t, won)
			require.False(t, first.Reclaimed, "a first claim is not a reclaim")

			now = now.Add(30 * time.Second)
			_, won, err = claim(s, key)
			require.NoError(t, err)
			require.False(t, won, "an unexpired claim is never taken over")

			now = now.Add(time.Minute)
			second, won, err := claim(s, key)
			require.NoError(t, err)
			require.True(t, won)
			require.True(t, second.Reclaimed)
			require.NotEqual(t, first.Token, second.Token, "the reclaim carries a fresh fence")
			outcome, reason := reclaimRow(t, s)
			require.Equal(t, string(model.WakeClaimed), outcome)
			require.Equal(t, model.WakeReasonReclaimedAfterCrash, reason)

			now = now.Add(2 * time.Minute)
			_, won, err = claim(s, key)
			require.NoError(t, err)
			require.False(t, won, "the retry cap is one")
			outcome, reason = reclaimRow(t, s)
			require.Equal(t, string(model.WakeUndeliverable), outcome)
			require.Equal(t, model.WakeReasonReclaimCap, reason)

			now = now.Add(time.Hour)
			_, won, err = claim(s, key)
			require.NoError(t, err)
			require.False(t, won, "a closed row is never claimed again")
		})
	}
}

func TestClaimWake_TerminalRow_IsNeverReclaimed(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 5, Epoch: 1}
	at, won, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeResult(t.Context(), at, model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}))
	now = now.Add(24 * time.Hour)
	_, won, err = s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.False(t, won, "a sent event never wakes again")
}
