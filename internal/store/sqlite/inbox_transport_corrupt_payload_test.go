// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecover_MalformedMailPayload_ReportsLoss(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET payload='{' WHERE rowid=(SELECT MIN(rowid) FROM sync_events WHERE op_type='comment')`)
	require.NoError(t, err)
	var path string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "missing.json"), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Len(t, result.Export.InboxJournal.Events, 2)
	require.Contains(t, strings.Join(result.Notes, " "), "unreadable", "a corrupt comment cannot be silently treated as definitely unaddressed")
}
