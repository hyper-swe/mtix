// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests independent cursor progress and late claims below an advanced cursor.
package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeCursor_ForeignHostOrUIDLiveClaim_DoesNotClamp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*model.WakeKey)
	}{
		{"other host", func(k *model.WakeKey) { k.HostID = "other-host" }},
		{"other uid", func(k *model.WakeKey) { k.UID = "other-uid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := wakeLedgerStore(t)
			target := wakeLedgerKey()
			foreign := target
			tc.change(&foreign)
			_, won, err := s.ClaimWake(t.Context(), foreign, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), target.HostID, target.UID, 100, time.Minute))
			cursor, err := s.WakeScanCursor(t.Context(), target.HostID, target.UID)
			require.NoError(t, err)
			require.Equal(t, int64(100), cursor)
			cursor, err = s.WakeScanCursor(t.Context(), foreign.HostID, foreign.UID)
			require.NoError(t, err)
			require.Zero(t, cursor)
		})
	}
}

func TestWakeCursor_LateLiveClaim_NeverRewindsProgress(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	key := wakeLedgerKey()
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), key.HostID, key.UID, 100, time.Minute))
	attempt, won, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won, "a registration may claim below the shared scan optimization")
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), key.HostID, key.UID, 120, time.Minute))
	cursor, err := s.WakeScanCursor(t.Context(), key.HostID, key.UID)
	require.NoError(t, err)
	require.Equal(t, int64(100), cursor, "a late live claim may clamp new progress but cannot rewind prior progress")
	require.NoError(t, s.RecordWakeOutcome(t.Context(), attempt, model.WakeDelivered))
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), key.HostID, key.UID, 120, time.Minute))
	cursor, err = s.WakeScanCursor(t.Context(), key.HostID, key.UID)
	require.NoError(t, err)
	require.Equal(t, int64(120), cursor, "progress resumes after the live claim completes")
}
