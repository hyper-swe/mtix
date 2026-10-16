// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests retained rejection evidence when the current attempt changes a terminal outcome.
package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestRecordWakeOutcome_TerminalChange_RetainsAuditAndOutcome(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	attempt, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeOutcome(t.Context(), attempt, model.WakeDelivered))
	for range 2 {
		require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), attempt, model.WakeError), model.ErrConflict)
		stats, err := s.WakeLedgerStats(t.Context())
		require.NoError(t, err)
		require.Equal(t, model.WakeLedgerStats{Rows: 1, Rejected: 1}, stats)
	}
	var token, claimed, outcome string
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), "SELECT attempt_token,claimed_at,outcome FROM wake_ledger").Scan(&token, &claimed, &outcome))
	require.Equal(t, attempt.Token, token)
	require.Equal(t, attempt.ClaimedAt.Format(time.RFC3339Nano), claimed)
	require.Equal(t, string(model.WakeDelivered), outcome)
	require.NoError(t, s.RecordWakeOutcome(t.Context(), attempt, model.WakeDelivered))
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1, Rejected: 1}, stats)
}
