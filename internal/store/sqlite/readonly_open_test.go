// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenDB_ReadOnlyExistingFile_OpensAndRejectsWrites: the read-only open
// reads the exact file under a special directory and refuses any write
// (MTIX-58, MTIX-112.2.4).
func TestOpenDB_ReadOnlyExistingFile_OpensAndRejectsWrites(t *testing.T) {
	for _, name := range append([]string{"plain"}, specialDirNames...) {
		t.Run(name, func(t *testing.T) {
			_, dir := specialDir(t, name)
			path := filepath.Join(dir, "mtix.db")
			s, err := New(path, slog.Default())
			require.NoError(t, err)
			insertOneNode(t, s, "TEST-1")
			require.NoError(t, s.Close())

			db, err := openDBWith(context.Background(), path, false, true, defaultOpenRetry())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			var n int
			require.NoError(t, db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM nodes`).Scan(&n))
			assert.Equal(t, 1, n)
			_, err = db.ExecContext(context.Background(), `DELETE FROM nodes`)
			require.Error(t, err, "a read-only handle must reject writes")
		})
	}
}

// TestOpenDB_ReadOnlyUnreadableFile_ErrorsAndCreatesNothing: a file the
// process cannot open is an error, not a reason to make another database.
func TestOpenDB_ReadOnlyUnreadableFile_ErrorsAndCreatesNothing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not deny reads here")
	}
	parent, dir := specialDir(t, "a?b")
	path := filepath.Join(dir, "mtix.db")
	s, err := New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	db, err := openDBWith(context.Background(), path, false, true, defaultOpenRetry())
	if err == nil {
		_ = db.Close()
	}
	require.Error(t, err)
	requireOnlyEntry(t, parent, "a?b")
	assert.NotContains(t, visibleEntries(t, dir), "mtix.db-journal")
}

// TestOpenDB_ReadOnlyOpen_CarriesModeRO pins that the never-create guarantee
// comes from the driver's own read-only mode, with no stat-then-open window.
func TestOpenDB_ReadOnlyOpen_CarriesModeRO(t *testing.T) {
	assert.Contains(t, readOnlyDSNParams(), "mode=ro")
	assert.Contains(t, readOnlyDSNParams(), "_pragma=query_only(1)")
}
