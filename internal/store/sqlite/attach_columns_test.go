// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"log/slog"
	"path/filepath"
	"testing"
)

// Independent column declarations cover each local table's stored contract.
func TestAttachSchema_Columns_MatchStoredContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db := newTestDB(t, path)
	expected := map[string][]string{
		"agent_registrations":     {"agent_id", "harness", "session_handle", "uid", "host_id", "pid", "pid_start_time", "permission_mode", "mode_seen_at", "origin", "epoch", "high_water", "pending_hold", "registered_at", "last_seen", "registrar_version", "attach_protocol", "principal_id", "returned_seq_mark"},
		"wake_ledger":             {"agent_id", "host_id", "uid", "event_seq", "epoch", "claimed_at", "outcome", "attempt_token"},
		"wake_outcome_rejections": {"agent_id", "host_id", "uid", "event_seq", "epoch", "attempt_token", "outcome", "rejected_at"},
		"wake_scan_cursor":        {"host_id", "uid", "cursor"},
		"dispatcher_heartbeat":    {"host_id", "uid", "pid", "protocol", "version", "seen_at"},
		"board_flags":             {"key", "value", "set_at", "source"},
		"attach_pending":          {"id", "kind", "board_key", "path", "payload", "queued_at"},
	}
	for table, want := range expected {
		t.Run(table, func(t *testing.T) {
			rows, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var got []string
			for rows.Next() {
				var name string
				require.NoError(t, rows.Scan(&name))
				got = append(got, name)
			}
			require.NoError(t, rows.Err())
			require.Equal(t, want, got)
		})
	}
}
func TestAttachSchema_ReleasedStoreUpgrade_PreservesLegacyIdentityAbsence(t *testing.T) {
	binary := compat054Binary(t)
	project := t.TempDir()
	attachReaderCommand(t, binary, project, "", "init", "--prefix", "TEST")
	attachReaderCommand(t, binary, project, "", "create", "legacy task")
	path := filepath.Join(project, ".mtix", "data", "mtix.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	id, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.Empty(t, id)
	var count int
	require.NoError(t, s.ReadDB().QueryRow("SELECT count(*) FROM nodes WHERE title='legacy task'").Scan(&count))
	require.Equal(t, 1, count)
	for _, table := range []string{"agent_registrations", "wake_ledger", "wake_scan_cursor", "dispatcher_heartbeat", "board_flags", "attach_pending"} {
		require.NoError(t, s.ReadDB().QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count))
		require.Equal(t, 1, count)
	}
}
func TestStoreInstanceID_IndependentCreations_UseDifferentIDs(t *testing.T) {
	var previous string
	for range 2 {
		s := newFreshTestStore(t)
		id, err := s.StoreInstanceID(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, id)
		require.NotEqual(t, previous, id)
		previous = id
	}
}
