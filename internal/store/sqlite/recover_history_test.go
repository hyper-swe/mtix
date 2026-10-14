// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRecover_HistoryMirror_LabelsFormat210AndKeepsOrphans: a recovered board
// whose mirror carries history (format 2.1.0) is written as 2.1.0 with the
// per-node entries, the orphan list and the entries the database still holds,
// and it verifies and imports.
func TestRecover_HistoryMirror_LabelsFormat210AndKeepsOrphans(t *testing.T) {
	f, raw := goldenStore(t)
	insertHistory(t, f.db, "01H0000000000000000000R001", goldenRootUID(t, raw), "status")
	insertHistory(t, f.db, "01H0000000000000000000R002", "ghost-uid", "status")
	f.s.SetHistoryExportSource(func() bool { return true })
	mirror, err := f.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	mirrorPath := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(mirrorPath, marshalExport(t, mirror), 0o600))
	// An entry only the database holds.
	insertHistory(t, f.db, "01H0000000000000000000R003", goldenRootUID(t, raw), "priority")

	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	require.Equal(t, sqlite.SchemaVersionHistory, res.Export.SchemaVersion)
	require.Len(t, res.Export.History, 1, "the orphan list is kept")
	got := 0
	for _, n := range res.Export.Nodes {
		got += len(n.History)
	}
	require.Equal(t, 2, got, "mirror entry plus the entry only the database holds")
	ok, err := sqlite.VerifyExportChecksum(res.Export)
	require.NoError(t, err)
	require.True(t, ok)
}

// TestRecover_PlainMirror_StaysFormat200: a mirror without history recovers
// as before, 2.0.0 and no history, even when the database holds entries.
func TestRecover_PlainMirror_StaysFormat200(t *testing.T) {
	f, raw := goldenStore(t)
	insertHistory(t, f.db, "01H0000000000000000000R004", goldenRootUID(t, raw), "status")
	mirrorPath := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(mirrorPath, raw, 0o600))
	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	require.Equal(t, sqlite.SchemaVersionV1, res.Export.SchemaVersion)
	require.Empty(t, res.Export.History)
	for _, n := range res.Export.Nodes {
		require.Empty(t, n.History)
	}
}
