// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests deterministic fence reuse refusal without changing process-wide entropy.
package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestClaimWake_ReusedFence_RefusesWithoutReplacingClaim(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	key := model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: 7, Epoch: 1}
	token := func() (string, error) { return "kept-fence", nil }
	first, won, err := s.claimWake(t.Context(), key, time.Minute, token)
	require.NoError(t, err)
	require.True(t, won)
	now = now.Add(time.Minute)
	second, won, err := s.claimWake(t.Context(), key, time.Minute, token)
	require.ErrorIs(t, err, model.ErrConflict)
	require.False(t, won)
	require.Empty(t, second.Token)
	var storedToken, claimed, outcome string
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), "SELECT attempt_token,claimed_at,outcome FROM wake_ledger").Scan(&storedToken, &claimed, &outcome))
	require.Equal(t, first.Token, storedToken)
	require.Equal(t, first.ClaimedAt.Format(time.RFC3339Nano), claimed)
	require.Equal(t, string(model.WakeClaimed), outcome)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1, Claimed: 1}, stats)
}
