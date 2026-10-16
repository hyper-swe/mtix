// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"github.com/stretchr/testify/require"
	"log/slog"
	"testing"
)

func TestAttachPending_ChangedRowSnapshot_DoesNotClearNewPayload(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	row := AttachPending{Kind: "lane", BoardKey: "board", Path: "own", Payload: "original", QueuedAt: "now"}
	row.ID, err = s.EnqueueAttachPending(t.Context(), row)
	require.NoError(t, err)
	rows, err := s.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Equal(t, []AttachPending{row}, rows)
	_, err = s.WriteDB().ExecContext(t.Context(), "UPDATE attach_pending SET payload=? WHERE id=?", "new", row.ID)
	require.NoError(t, err)
	removed, err := s.DeleteAttachPending(t.Context(), row)
	require.NoError(t, err)
	require.False(t, removed)
	rows, err = s.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "new", rows[0].Payload)
	removed, err = s.DeleteAttachPending(t.Context(), rows[0])
	require.NoError(t, err)
	require.True(t, removed)
	removed, err = s.DeleteAttachPending(t.Context(), rows[0])
	require.NoError(t, err)
	require.False(t, removed)
}
func TestAttachPending_ClosedStore_ReportsPersistenceErrors(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = s.EnqueueAttachPending(t.Context(), AttachPending{})
	require.Error(t, err)
	_, err = s.ListAttachPending(t.Context())
	require.Error(t, err)
	_, err = s.DeleteAttachPending(t.Context(), AttachPending{})
	require.Error(t, err)
}

func TestListAttachPending_MalformedStoredID_ReturnsScanError(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err = s.WriteDB().ExecContext(t.Context(), "DROP TABLE attach_pending")
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), "CREATE TABLE attach_pending(id TEXT,kind TEXT,board_key TEXT,path TEXT,payload TEXT,queued_at TEXT)")
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), "INSERT INTO attach_pending VALUES(?,?,?,?,?,?)", "not-an-integer", "lane", "board", "path", "payload", "now")
	require.NoError(t, err)
	_, err = s.ListAttachPending(t.Context())
	require.ErrorContains(t, err, "scan attach side write")
}
