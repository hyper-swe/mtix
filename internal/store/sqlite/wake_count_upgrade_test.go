// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

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

func TestNew_PreCountWakeSchema_InitializesLedgerAndInboxIndex(t *testing.T) {
	path, attempt := preCountWakeFixture(t)
	for _, name := range []string{"upgrade", "idempotent reopen"} {
		t.Run(name, func(t *testing.T) {
			s, err := sqlite.New(path, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			assertWakeCountUpgrade(t, s, attempt, true)
		})
	}
}

func preCountWakeFixture(t *testing.T) (string, model.WakeAttempt) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre-count-wake.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	s.SetClock(func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) })
	attempt, won, err := s.ClaimWake(t.Context(), wakeLedgerKey(), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	// The pre-count .7 store has these wake tables, but no addressee index.
	_, err = s.WriteDB().ExecContext(t.Context(), `DROP INDEX IF EXISTS idx_sync_events_comment_addressee`)
	require.NoError(t, err)
	assertWakeCountUpgrade(t, s, attempt, false)
	return path, attempt
}

func assertWakeCountUpgrade(t *testing.T, s *sqlite.Store, attempt model.WakeAttempt, indexed bool) {
	t.Helper()
	var tables, indexes int
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_master WHERE type=? AND name IN (?,?)`,
		"table", "wake_ledger", "wake_outcome_rejections").Scan(&tables))
	require.Equal(t, 2, tables)
	var token, outcome, version string
	k := attempt.Key
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(),
		`SELECT attempt_token,outcome FROM wake_ledger WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
		k.AgentID, k.HostID, k.UID, k.EventSeq, k.Epoch).Scan(&token, &outcome))
	require.Equal(t, attempt.Token, token)
	require.Equal(t, string(model.WakeClaimed), outcome)
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(),
		`SELECT value FROM meta WHERE key=?`, "schema_version").Scan(&version))
	require.Equal(t, "4", version)
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`,
		"index", "idx_sync_events_comment_addressee").Scan(&indexes))
	if indexed {
		require.Equal(t, 1, indexes)
	} else {
		require.Zero(t, indexes)
	}
}
