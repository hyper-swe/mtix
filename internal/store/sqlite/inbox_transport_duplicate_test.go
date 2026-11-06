// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRecover_DamagedDuplicateIdentity_ProducesValidArtifact(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	// A damaged/reconstructed table can retain readable rows without its
	// uniqueness constraint. Recovery must not emit an unimportable journal.
	for _, statement := range []string{
		`CREATE TABLE journal_without_index AS SELECT * FROM sync_events`,
		`DROP TABLE sync_events`,
		`ALTER TABLE journal_without_index RENAME TO sync_events`,
		`INSERT INTO sync_events SELECT * FROM sync_events WHERE rowid=(SELECT MIN(rowid) FROM sync_events WHERE op_type='comment')`,
	} {
		_, err := raw.Exec(statement)
		require.NoError(t, err)
	}
	var path string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "missing.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.Equal(t, 3, result.Export.NodeCount)
	require.True(t, result.InboxIncomplete, "duplicate identity is evidence of damage")
	require.NoError(t, sqlite.ValidateExport(result.Export), "readable duplicates must be reconciled or reported/omitted, not make the whole salvage unimportable")
}
