// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests invalid requests, cancellation and storage faults without losing error causes.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestWakeLedger_InvalidClaimKeys_DoNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*model.WakeKey)
		lease  time.Duration
	}{
		{"agent", func(k *model.WakeKey) { k.AgentID = "" }, time.Minute},
		{"host", func(k *model.WakeKey) { k.HostID = "" }, time.Minute},
		{"uid", func(k *model.WakeKey) { k.UID = "" }, time.Minute},
		{"sequence", func(k *model.WakeKey) { k.EventSeq = 0 }, time.Minute},
		{"epoch", func(k *model.WakeKey) { k.Epoch = -1 }, time.Minute},
		{"lease", func(*model.WakeKey) {}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := wakeLedgerStore(t)
			key := wakeLedgerKey()
			tc.change(&key)
			_, won, err := s.ClaimWake(t.Context(), key, tc.lease)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.False(t, won)
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Equal(t, model.WakeLedgerStats{}, stats)
		})
	}
}

func TestRecordWakeOutcome_InvalidOutcomeOrFence_DoesNotFinalize(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	claim, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	for _, outcome := range []model.WakeOutcome{model.WakeClaimed, "", "unknown"} {
		require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), claim, outcome), model.ErrInvalidInput)
	}
	empty := claim
	empty.Token = ""
	require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), empty, model.WakeDelivered), model.ErrInvalidInput)
	empty = claim
	empty.Key.HostID = ""
	require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), empty, model.WakeDelivered), model.ErrInvalidInput)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1, Claimed: 1}, stats)
}

func TestWakeLedger_CancelledContext_PreservesCause(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	claim, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, operation := range []func() error{
		func() error { _, _, err := s.ClaimWake(ctx, wakeLedgerKey(), time.Minute); return err },
		func() error { return s.RecordWakeOutcome(ctx, claim, model.WakeDelivered) },
		func() error { _, err := s.StaleWakeClaims(ctx, time.Minute); return err },
		func() error { _, err := s.WakeLedgerStats(ctx); return err },
		func() error { return s.AdvanceWakeScanCursor(ctx, "test-host", "test-uid", 10, time.Minute) },
		func() error { _, err := s.WakeScanCursor(ctx, "test-host", "test-uid"); return err },
	} {
		require.ErrorIs(t, operation(), context.Canceled)
	}
}

func TestWakeLedger_MalformedClaimTime_RefusesWithoutReplacement(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	claim, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	_, err = s.WriteDB().Exec("UPDATE wake_ledger SET claimed_at=?", "invalid")
	require.NoError(t, err)
	_, _, err = s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.ErrorContains(t, err, "parse wake claim time")
	_, err = s.StaleWakeClaims(t.Context(), time.Minute)
	require.ErrorContains(t, err, "parse wake claim time")
	require.ErrorContains(t, s.AdvanceWakeScanCursor(t.Context(), "test-host", "test-uid", 100, time.Minute), "parse wake claim time")
	require.ErrorContains(t, s.RecordWakeOutcome(t.Context(), claim, model.WakeDelivered), "parse wake claim time")
	var token, outcome string
	require.NoError(t, s.ReadDB().QueryRow("SELECT attempt_token,outcome FROM wake_ledger").Scan(&token, &outcome))
	require.Equal(t, claim.Token, token)
	require.Equal(t, "claimed", outcome)
}

func TestWakeLedger_AbsentTables_ReturnContextualErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		run  func(context.Context, *sqlite.Store, model.WakeAttempt) error
	}{
		{"claim read", "DROP TABLE wake_ledger", func(ctx context.Context, s *sqlite.Store, a model.WakeAttempt) error {
			_, _, err := s.ClaimWake(ctx, a.Key, time.Minute)
			return err
		}},
		{"outcome read", "DROP TABLE wake_ledger", func(ctx context.Context, s *sqlite.Store, a model.WakeAttempt) error {
			return s.RecordWakeOutcome(ctx, a, model.WakeDelivered)
		}},
		{"stale scan", "DROP TABLE wake_ledger", func(ctx context.Context, s *sqlite.Store, _ model.WakeAttempt) error {
			_, err := s.StaleWakeClaims(ctx, time.Minute)
			return err
		}},
		{"counts", "DROP TABLE wake_outcome_rejections", func(ctx context.Context, s *sqlite.Store, _ model.WakeAttempt) error {
			_, err := s.WakeLedgerStats(ctx)
			return err
		}},
		{"cursor read", "DROP TABLE wake_scan_cursor", func(ctx context.Context, s *sqlite.Store, _ model.WakeAttempt) error {
			_, err := s.WakeScanCursor(ctx, "test-host", "test-uid")
			return err
		}},
		{"cursor scan", "DROP TABLE wake_ledger", func(ctx context.Context, s *sqlite.Store, _ model.WakeAttempt) error {
			return s.AdvanceWakeScanCursor(ctx, "test-host", "test-uid", 100, time.Minute)
		}},
		{"cursor write", "DROP TABLE wake_scan_cursor", func(ctx context.Context, s *sqlite.Store, _ model.WakeAttempt) error {
			return s.AdvanceWakeScanCursor(ctx, "test-host", "test-uid", 100, time.Minute)
		}},
		{"rejection audit", "DROP TABLE wake_outcome_rejections", func(ctx context.Context, s *sqlite.Store, a model.WakeAttempt) error {
			a.Token = "stale-attempt"
			return s.RecordWakeOutcome(ctx, a, model.WakeError)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := wakeLedgerStore(t)
			claim, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
			require.NoError(t, err)
			// Each fixture statement is constant DDL, with no user-controlled SQL.
			_, err = s.WriteDB().Exec(tc.sql)
			require.NoError(t, err)
			require.ErrorContains(t, tc.run(t.Context(), s, claim), "wake")
		})
	}
}

func TestRecordWakeOutcome_StaleReplay_DoesNotReplaceNewerClaim(t *testing.T) {
	s, now := wakeLedgerStore(t)
	first, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	*now = now.Add(time.Minute)
	second, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	for range 3 {
		require.ErrorIs(t, s.RecordWakeOutcome(t.Context(), first, model.WakeDelivered), model.ErrConflict)
	}
	var token, claimed, outcome string
	require.NoError(t, s.ReadDB().QueryRow("SELECT attempt_token,claimed_at,outcome FROM wake_ledger").Scan(&token, &claimed, &outcome))
	require.Equal(t, second.Token, token)
	require.Equal(t, second.ClaimedAt.Format(time.RFC3339Nano), claimed)
	require.Equal(t, "claimed", outcome)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1, Claimed: 1, Rejected: 1}, stats)
}

func TestWakeLedger_MalformedSequence_RefusesScans(t *testing.T) {
	for _, value := range []any{"invalid", int64(0)} {
		s, _ := wakeLedgerStore(t)
		_, _, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
		require.NoError(t, err)
		_, err = s.WriteDB().Exec("UPDATE wake_ledger SET event_seq=?", value)
		require.NoError(t, err)
		require.Error(t, s.AdvanceWakeScanCursor(t.Context(), "test-host", "test-uid", 100, time.Minute))
		if _, invalid := value.(string); invalid {
			_, err = s.StaleWakeClaims(t.Context(), time.Minute)
			require.ErrorContains(t, err, "scan wake claim")
		}
	}
}
