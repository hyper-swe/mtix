// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestReadJournalSince_ReadOnlyStoreWithoutMarkerTable_HonoursLimit opens a
// legacy database (no skip table) through the read-only path used on unsafe
// filesystems, where init never runs, and requires the fallback to read the
// journal and respect the page limit.
func TestReadJournalSince_ReadOnlyStoreWithoutMarkerTable_HonoursLimit(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := New(path, slog.Default())
	require.NoError(t, err)
	for _, id := range []string{"TEST-1", "TEST-2", "TEST-3"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "TEST", Title: "t " + id, Status: model.StatusOpen,
			NodeType: model.NodeTypeEpic, Priority: model.PriorityMedium, Weight: 1,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), ContentHash: "h",
		}))
	}
	_, err = s.WriteDB().ExecContext(ctx, `DROP TABLE hook_suppressed_events`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	ro, err := openReadOnlyStore(ctx, path, slog.Default(), defaultOpenRetry(), "macfuse")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })
	require.True(t, ro.writeRefused, "the store must really be read-only")

	all, err := ro.ReadJournalSince(ctx, 0, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 3)
	page, err := ro.ReadJournalSince(ctx, 0, 2)
	require.NoError(t, err)
	require.Len(t, page, 2, "the fallback path must apply the page limit")
	require.Equal(t, all[0].Seq, page[0].Seq)
}
