// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests free scan progress and recovery independent of held registrations.
package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeCursor_HeldRegistration_DoesNotStall(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	_, err := s.WriteDB().Exec(`INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,pending_hold)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, "held-agent", "test", "test-uid", "test-host", "test", "now", "now", "test", 1, 1)
	require.NoError(t, err)
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), "test-host", "test-uid", 100, time.Minute))
	cursor, err := s.WakeScanCursor(t.Context(), "test-host", "test-uid")
	require.NoError(t, err)
	require.Equal(t, int64(100), cursor)
	_, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won, "a registration may claim an event below the shared optimization cursor")
}

func TestWakeCursor_LiveClaimClamps_ExpiredClaimDoesNot(t *testing.T) {
	s, now := wakeLedgerStore(t)
	claim, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), "test-host", "test-uid", 100, time.Minute))
	cursor, err := s.WakeScanCursor(t.Context(), "test-host", "test-uid")
	require.NoError(t, err)
	require.Equal(t, int64(6), cursor)
	*now = now.Add(time.Minute)
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), "test-host", "test-uid", 100, time.Minute))
	cursor, err = s.WakeScanCursor(t.Context(), "test-host", "test-uid")
	require.NoError(t, err)
	require.Equal(t, int64(100), cursor)
	stale, err := s.StaleWakeClaims(t.Context(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []model.WakeAttempt{claim}, stale)
}

func TestWakeCursor_InvalidArguments_LeaveAbsentCursorUnchanged(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	cursor, err := s.WakeScanCursor(t.Context(), "test-host", "test-uid")
	require.NoError(t, err)
	require.Zero(t, cursor)
	for _, tc := range []struct {
		host, uid string
		target    int64
		lease     time.Duration
	}{
		{"", "test-uid", 10, time.Minute},
		{"test-host", "", 10, time.Minute},
		{"test-host", "test-uid", -1, time.Minute},
		{"test-host", "test-uid", 10, 0},
	} {
		require.ErrorIs(t, s.AdvanceWakeScanCursor(t.Context(), tc.host, tc.uid, tc.target, tc.lease), model.ErrInvalidInput)
	}
	_, err = s.StaleWakeClaims(t.Context(), 0)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	var count int
	require.NoError(t, s.ReadDB().QueryRow("SELECT count(*) FROM wake_scan_cursor").Scan(&count))
	require.Zero(t, count)
}
