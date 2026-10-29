// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHubEpochFault_PublicMarkEpochWriteRollbackAndRetry(t *testing.T) {
	s, raw := applyTestStore(t)
	callbacks := 0
	s.SetOnCommit(func() { callbacks++ })
	_, err := raw.ExecContext(t.Context(), `CREATE TRIGGER contract_epoch_abort BEFORE INSERT ON board_flags WHEN NEW.key = 'hub_connected_epoch' BEGIN SELECT RAISE(ABORT,'synthetic epoch fault'); END`)
	require.NoError(t, err)
	flipped, err := s.MarkHubConnected(t.Context(), "config")
	require.ErrorContains(t, err, "store hub mark epoch")
	require.False(t, flipped)
	var count int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM board_flags WHERE key IN (?,?)`, hubMarkKey, hubEpochKey).Scan(&count))
	require.Zero(t, count)
	require.Zero(t, callbacks)
	mark, err := s.HubMark(t.Context())
	require.NoError(t, err)
	require.Equal(t, HubMark{}, mark)
	_, err = raw.ExecContext(t.Context(), `DROP TRIGGER contract_epoch_abort`)
	require.NoError(t, err)
	flipped, err = s.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.True(t, flipped)
	mark, err = s.HubMark(t.Context())
	require.NoError(t, err)
	require.True(t, mark.Set)
	require.NotEmpty(t, mark.Epoch)
	require.Equal(t, 1, callbacks)
}
func TestHubEpochFault_ForeignEpochWriteRollbackLeavesNoNodeOrEvidence(t *testing.T) {
	s, raw := applyTestStore(t)
	_, err := raw.ExecContext(t.Context(), `CREATE TRIGGER contract_epoch_abort BEFORE INSERT ON board_flags WHEN NEW.key = 'hub_connected_epoch' BEGIN SELECT RAISE(ABORT,'synthetic epoch fault'); END`)
	require.NoError(t, err)
	event := foreignCreate(t, "MTIX-1", 1)
	require.Error(t, applyOnce(t, s, event))
	for _, tc := range []struct{ table, query string }{
		{"nodes", `SELECT COUNT(*) FROM nodes`},
		{"sync_events", `SELECT COUNT(*) FROM sync_events`},
		{"applied_events", `SELECT COUNT(*) FROM applied_events`},
		{"board_flags", `SELECT COUNT(*) FROM board_flags`},
	} {
		var count int
		require.NoError(t, raw.QueryRowContext(t.Context(), tc.query).Scan(&count))
		require.Zero(t, count, tc.table)
	}
	_, err = raw.ExecContext(t.Context(), `DROP TRIGGER contract_epoch_abort`)
	require.NoError(t, err)
	require.NoError(t, applyOnce(t, s, event))
	mark, err := s.HubMark(t.Context())
	require.NoError(t, err)
	require.True(t, mark.Set)
	require.NotEmpty(t, mark.Epoch)
}
