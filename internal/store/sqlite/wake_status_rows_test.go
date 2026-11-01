// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the read-only wake status listing used by inbox status (MTIX-112.40.6).
package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeStatusRows_ScopedNewestFirstAndLimited(t *testing.T) {
	s := newTestStore(t)
	s.SetClock(func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) })
	for _, seq := range []int64{1, 2, 3} {
		key := model.WakeKey{AgentID: "worker", HostID: "h1", UID: "u1", EventSeq: seq, Epoch: 1}
		attempt, won, err := s.ClaimWake(t.Context(), key, time.Minute)
		require.NoError(t, err)
		require.True(t, won)
		require.NoError(t, s.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}))
	}
	other := model.WakeKey{AgentID: "other", HostID: "h1", UID: "u1", EventSeq: 9, Epoch: 1}
	_, won, err := s.ClaimWake(t.Context(), other, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	foreign := model.WakeKey{AgentID: "worker", HostID: "h2", UID: "u1", EventSeq: 5, Epoch: 1}
	_, _, err = s.ClaimWake(t.Context(), foreign, time.Minute)
	require.NoError(t, err)

	rows, err := s.WakeStatusRows(t.Context(), "h1", "u1", "worker", 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, int64(3), rows[0].EventSeq)
	require.Equal(t, int64(2), rows[1].EventSeq)
	require.Equal(t, model.WakeSent, rows[0].Outcome)
	require.Equal(t, "hook-waiter", rows[0].Route)
	require.NotEmpty(t, rows[0].OutcomeAt)
	require.NotEmpty(t, rows[0].ClaimedAt)
}

func TestWakeStatusRows_InvalidInput_Refused(t *testing.T) {
	s := newTestStore(t)
	for _, tt := range []struct {
		name, host, uid, agent string
		limit                  int
	}{{"empty host", "", "u", "a", 1}, {"empty agent", "h", "u", "", 1}, {"zero limit", "h", "u", "a", 0}, {"huge limit", "h", "u", "a", 5000}} {
		_, err := s.WakeStatusRows(t.Context(), tt.host, tt.uid, tt.agent, tt.limit)
		require.ErrorIs(t, err, model.ErrInvalidInput, tt.name)
	}
}

func TestWakeStatusRows_LegacyRowWithoutOutcomeTime_IsListed(t *testing.T) {
	s := newTestStore(t)
	key := model.WakeKey{AgentID: "worker", HostID: "h1", UID: "u1", EventSeq: 1, Epoch: 1}
	attempt, _, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.NoError(t, s.RecordWakeOutcome(t.Context(), attempt, model.WakeDelivered))
	rows, err := s.WakeStatusRows(t.Context(), "h1", "u1", "worker", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, model.WakeDelivered, rows[0].Outcome)
	require.Empty(t, rows[0].OutcomeAt)
}
