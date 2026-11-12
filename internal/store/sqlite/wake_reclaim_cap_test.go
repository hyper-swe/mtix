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

// A reclaimed event that a route-bearing result then finalizes keeps the reclaimed-after-crash
// marker, so the ledger and the doctor count the one permitted duplicate (MTIX-112.40.13).
func TestRecordWakeResult_ReclaimedClaim_KeepsMarkerAcrossRouteResults(t *testing.T) {
	for name, result := range map[string]model.WakeResult{
		"route and reason": {Outcome: model.WakeSent, Route: "socket-push", Reason: "frame-written"},
		"route only":       {Outcome: model.WakeSent, Route: "socket-push"},
		"bare outcome":     {Outcome: model.WakeSent},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := sqlite.New(t.TempDir(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
			s.SetClock(func() time.Time { return now })
			key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 5, Epoch: 1}
			_, won, err := s.ClaimWake(t.Context(), key, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			now = now.Add(2 * time.Minute)
			second, won, err := s.ClaimWake(t.Context(), key, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			require.True(t, second.Reclaimed)
			require.NoError(t, s.RecordWakeResult(t.Context(), second, result))
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.EqualValues(t, 1, stats.Reclaimed, "the reclaim stays counted after the redelivery is recorded")
			outcome, reason := reclaimRow(t, s)
			require.Equal(t, string(model.WakeSent), outcome)
			require.Equal(t, model.WakeReasonReclaimedAfterCrash, reason)
		})
	}
}

// A first claim finalized with a result keeps that result's own reason.
func TestRecordWakeResult_FirstClaim_KeepsResultReason(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 5, Epoch: 1}
	first, won, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeResult(t.Context(), first, model.WakeResult{Outcome: model.WakeSent, Route: "socket-push", Reason: "frame-written"}))
	_, reason := reclaimRow(t, s)
	require.Equal(t, "frame-written", reason)
}

// A reclaimed event whose redelivery then fails records the failure's real reason: the
// marker counts a redelivery that happened, not one that errored (MTIX-112.40.13).
func TestRecordWakeResult_ReclaimedClaim_FailedResultRecordsRealReason(t *testing.T) {
	for name, tc := range map[string]struct {
		result model.WakeResult
		want   string
	}{
		"undeliverable with send-error": {model.WakeResult{Outcome: model.WakeUndeliverable, Reason: "send-error"}, "send-error"},
		"undeliverable with reason":     {model.WakeResult{Outcome: model.WakeUndeliverable, Reason: "socket-closed"}, "socket-closed"},
		"error outcome":                 {model.WakeResult{Outcome: model.WakeError, Reason: "send-error"}, "send-error"},
		"held":                          {model.WakeResult{Outcome: model.WakeHeld, Reason: "hop-depth"}, "hop-depth"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := sqlite.New(t.TempDir(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
			s.SetClock(func() time.Time { return now })
			key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", EventSeq: 5, Epoch: 1}
			_, won, err := s.ClaimWake(t.Context(), key, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			now = now.Add(2 * time.Minute)
			second, won, err := s.ClaimWake(t.Context(), key, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			require.True(t, second.Reclaimed)
			require.NoError(t, s.RecordWakeResult(t.Context(), second, tc.result))
			_, reason := reclaimRow(t, s)
			require.Equal(t, tc.want, reason, "a failed redelivery keeps its own reason")
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Zero(t, stats.Reclaimed, "no redelivery happened, so no duplicate is counted")
		})
	}
}
