// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests adapter outcome persistence, the confirmed upgrade and the additive-column
// compatibility of the wake ledger (MTIX-112.40.2).
package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func wakeResultKey() model.WakeKey {
	return model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: 9, Epoch: 1}
}

func claimWakeForResult(t *testing.T, s *sqlite.Store) model.WakeAttempt {
	t.Helper()
	attempt, won, err := s.ClaimWake(t.Context(), wakeResultKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	return attempt
}

func TestRecordWakeResult_States_PersistAcrossReopen(t *testing.T) {
	tests := []struct {
		name string
		in   model.WakeResult
		want model.WakeResult
	}{
		{"sent", model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}, model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}},
		{"held", model.WakeResult{Outcome: model.WakeHeld, Route: "claude-socket", Reason: "peer-approval"}, model.WakeResult{Outcome: model.WakeHeld, Route: "claude-socket", Reason: "peer-approval"}},
		{"undeliverable", model.WakeResult{Outcome: model.WakeUndeliverable, Route: "inbox-only", Reason: "no-live-route"}, model.WakeResult{Outcome: model.WakeUndeliverable, Route: "inbox-only", Reason: "no-live-route"}},
		{"off", model.WakeResult{Outcome: model.WakeOff, Route: "inbox-only"}, model.WakeResult{Outcome: model.WakeOff, Route: "inbox-only"}},
		{"legacy delivered reads as sent", model.WakeResult{Outcome: model.WakeDelivered}, model.WakeResult{Outcome: model.WakeSent}},
		{"legacy error reads as undeliverable", model.WakeResult{Outcome: model.WakeError}, model.WakeResult{Outcome: model.WakeUndeliverable, Reason: "send-error"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wake.db")
			now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			s, err := sqlite.New(path, slog.Default())
			require.NoError(t, err)
			s.SetClock(func() time.Time { return now })
			attempt := claimWakeForResult(t, s)
			require.NoError(t, s.RecordWakeResult(t.Context(), attempt, tt.in))
			require.NoError(t, s.RecordWakeResult(t.Context(), attempt, tt.in), "same result twice is idempotent")
			require.NoError(t, s.Close())

			s2, err := sqlite.New(path, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s2.Close()) })
			got, found, err := s2.ReadWakeResult(t.Context(), wakeResultKey())
			require.NoError(t, err)
			require.True(t, found)
			wantAt := ""
			if tt.in.Route != "" || tt.in.Reason != "" {
				wantAt = now.Format(time.RFC3339Nano)
			}
			if tt.in.Route == "" && tt.in.Reason == "" {
				got.At = ""
			}
			tt.want.At = wantAt
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRecordWakeResult_InvalidInput_Refused(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	attempt := claimWakeForResult(t, s)
	for name, r := range map[string]model.WakeResult{
		"confirmed":               {Outcome: model.WakeConfirmed},
		"claimed":                 {Outcome: model.WakeClaimed},
		"unknown":                 {Outcome: "bogus"},
		"undeliverable no reason": {Outcome: model.WakeUndeliverable},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, s.RecordWakeResult(t.Context(), attempt, r), model.ErrInvalidInput)
		})
	}
	attempt.Token = ""
	require.ErrorIs(t, s.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent}), model.ErrInvalidInput)
}

func TestRecordWakeResult_StaleFence_RejectedWithoutOverwrite(t *testing.T) {
	s, now := wakeLedgerStore(t)
	first := claimWakeForResult(t, s)
	*now = now.Add(2 * time.Minute)
	second, won, err := s.ClaimWake(t.Context(), wakeResultKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.ErrorIs(t, s.RecordWakeResult(t.Context(), first, model.WakeResult{Outcome: model.WakeSent, Route: "x"}), model.ErrConflict)
	require.NoError(t, s.RecordWakeResult(t.Context(), second, model.WakeResult{Outcome: model.WakeHeld, Route: "y", Reason: "peer-approval"}))
	got, _, err := s.ReadWakeResult(t.Context(), wakeResultKey())
	require.NoError(t, err)
	require.Equal(t, model.WakeHeld, got.Outcome)
	require.Equal(t, "y", got.Route)
}

func TestUpgradeWakeConfirmed_Transitions(t *testing.T) {
	tests := []struct {
		name    string
		result  *model.WakeResult
		wantErr error
		want    model.WakeOutcome
	}{
		{"no row", nil, model.ErrNotFound, ""},
		{"sent upgrades", &model.WakeResult{Outcome: model.WakeSent, Route: "r"}, nil, model.WakeConfirmed},
		{"legacy delivered upgrades", &model.WakeResult{Outcome: model.WakeDelivered}, nil, model.WakeConfirmed},
		{"held is not upgraded", &model.WakeResult{Outcome: model.WakeHeld, Route: "r"}, model.ErrInvalidTransition, model.WakeHeld},
		{"undeliverable is not upgraded", &model.WakeResult{Outcome: model.WakeUndeliverable, Route: "r", Reason: "x"}, model.ErrInvalidTransition, model.WakeUndeliverable},
		{"off is not upgraded", &model.WakeResult{Outcome: model.WakeOff}, model.ErrInvalidTransition, model.WakeOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := wakeLedgerStore(t)
			if tt.result != nil {
				attempt := claimWakeForResult(t, s)
				require.NoError(t, s.RecordWakeResult(t.Context(), attempt, *tt.result))
			}
			err := s.UpgradeWakeConfirmed(t.Context(), wakeResultKey())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.NoError(t, s.UpgradeWakeConfirmed(t.Context(), wakeResultKey()), "idempotent")
			}
			if tt.result == nil {
				stats, statErr := s.WakeLedgerStats(t.Context())
				require.NoError(t, statErr)
				require.Zero(t, stats.Rows, "upgrade never creates a claim")
				return
			}
			got, _, readErr := s.ReadWakeResult(t.Context(), wakeResultKey())
			require.NoError(t, readErr)
			want := tt.want.Canonical()
			require.Equal(t, want, got.Outcome)
		})
	}
}

func TestUpgradeWakeConfirmed_AfterConfirm_NeverDowngradesOnLateResult(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	attempt := claimWakeForResult(t, s)
	require.NoError(t, s.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "r"}))
	require.NoError(t, s.UpgradeWakeConfirmed(t.Context(), wakeResultKey()))
	require.ErrorIs(t, s.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "r"}), model.ErrConflict)
	got, _, err := s.ReadWakeResult(t.Context(), wakeResultKey())
	require.NoError(t, err)
	require.Equal(t, model.WakeConfirmed, got.Outcome)
}

func TestWakeLedger_ResultColumns_AdditiveAndLegacyStatementsStillWork(t *testing.T) {
	s, _ := wakeLedgerStore(t)
	cols := map[string]bool{}
	rows, err := s.ReadDB().QueryContext(t.Context(), "SELECT name FROM pragma_table_info('wake_ledger')")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		cols[n] = true
	}
	require.NoError(t, rows.Err())
	for _, c := range []string{"agent_id", "attempt_token", "route", "reason", "outcome_at"} {
		require.True(t, cols[c], c)
	}
	// An older binary names its own columns, so its INSERT and SELECT run unchanged.
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO wake_ledger
		(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome,attempt_token) VALUES(?,?,?,?,?,?,?,?)`,
		"old", "h", "u", 1, 0, "2000-01-01T00:00:00Z", "delivered", "tok")
	require.NoError(t, err)
	var outcome string
	require.NoError(t, s.ReadDB().QueryRow(`SELECT attempt_token,claimed_at,outcome FROM wake_ledger WHERE agent_id='old'`).Scan(new(string), new(string), &outcome))
	require.Equal(t, "delivered", outcome)
	got, found, err := s.ReadWakeResult(t.Context(), model.WakeKey{AgentID: "old", HostID: "h", UID: "u", EventSeq: 1})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, model.WakeSent, got.Outcome)
}

func TestWakeLedger_Reclaim_ClearsStaleRouteReasonAndTime(t *testing.T) {
	s, now := wakeLedgerStore(t)
	_ = claimWakeForResult(t, s)
	// Seed leftovers on a still-claimed row, as a defect or foreign writer could.
	_, err := s.WriteDB().ExecContext(t.Context(), `UPDATE wake_ledger SET route='stale-route',reason='stale-reason',outcome_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	*now = now.Add(2 * time.Minute)
	_ = claimWakeForResult(t, s) // lease expired: the claimed row is reclaimed
	var route, reason, at string
	require.NoError(t, s.ReadDB().QueryRow(`SELECT route,reason,outcome_at FROM wake_ledger`).Scan(&route, &reason, &at))
	require.Empty(t, route+reason+at)
}

func TestUpgradeWakeConfirmed_RecordsConfirmTime(t *testing.T) {
	s, now := wakeLedgerStore(t)
	attempt := claimWakeForResult(t, s)
	require.NoError(t, s.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "r"}))
	*now = now.Add(5 * time.Second)
	require.NoError(t, s.UpgradeWakeConfirmed(t.Context(), wakeResultKey()))
	got, _, err := s.ReadWakeResult(t.Context(), wakeResultKey())
	require.NoError(t, err)
	require.Equal(t, now.Format(time.RFC3339Nano), got.At)
	require.Equal(t, "r", got.Route, "the route survives the upgrade")
}
