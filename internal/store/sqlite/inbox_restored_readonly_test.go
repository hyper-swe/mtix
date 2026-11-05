// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"github.com/hyper-swe/mtix/internal/model"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// FR-19.1: legacy read-only databases have no restored marker table and cannot migrate.
func TestRestoredInbox_LegacyReadOnlyReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Title: "legacy", Status: model.StatusOpen, NodeType: model.NodeTypeEpic, Priority: model.PriorityMedium, Weight: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), ContentHash: "h"}))
	_, err = s.WriteDB().ExecContext(t.Context(), `DROP TABLE inbox_restored_events`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	ro, err := openReadOnlyStore(t.Context(), path, slog.Default(), defaultOpenRetry(), "macfuse")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })
	require.True(t, ro.writeRefused)
	counted, err := ro.CountSyncEvents(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, counted)
	events, err := ro.ReadRelayJournalSince(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	_, err = ro.PermissionAddressedAgents(t.Context())
	require.NoError(t, err)
	_, err = ro.ReplyByEvents(t.Context(), 0, 10)
	require.NoError(t, err)
	_, err = ro.HasSyncedWithHub(t.Context())
	require.NoError(t, err)
	var count int
	require.NoError(t, ro.QueryRow(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE name='inbox_restored_events'`).Scan(&count))
	require.Zero(t, count, "reads cannot create the missing table")
}
