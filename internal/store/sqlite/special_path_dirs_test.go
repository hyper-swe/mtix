// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specialDirNames are directory names that end or rewrite a naive SQLite
// DSN (MTIX-112.2.4): ? starts the query string, # starts a fragment, % starts
// an escape, and a single quote ends a SQL string literal.
//
// The set is chosen for the host: a question mark is not a legal Windows file
// name, so the "a?b" row cannot be created there and is left out of the list
// rather than skipped (the windows-runtime job rejects any skipped test).
var specialDirNames = specialDirNamesFor(runtime.GOOS)

// specialDirNamesFor returns the special directory names a host of the given
// GOOS can create. Windows forbids ? in a file name.
func specialDirNamesFor(goos string) []string {
	names := []string{"a?b", "c#d", "e%f", "e%3fx", "it's q", "sp ace"}
	if goos != "windows" {
		return names
	}
	var out []string
	for _, n := range names {
		if !strings.Contains(n, "?") {
			out = append(out, n)
		}
	}
	return out
}

// specialDir makes <tmp>/<name> and returns the parent and the directory.
func specialDir(t *testing.T, name string) (parent, dir string) {
	t.Helper()
	parent = t.TempDir()
	dir = filepath.Join(parent, name)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	return parent, dir
}

// requireOnlyEntry fails when parent holds anything but the one directory:
// a wrong-database open leaves a stray sibling file behind.
func requireOnlyEntry(t *testing.T, parent, name string) {
	t.Helper()
	got := visibleEntries(t, parent)
	require.Equal(t, []string{name}, got, "no database may be created beside the directory")
}

// visibleEntries lists dir without the "._*" AppleDouble companions that
// macOS writes on non-APFS volumes; they are not databases.
func visibleEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var got []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "._") {
			got = append(got, e.Name())
		}
	}
	return got
}

func insertOneNode(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, parent_id, depth, seq, project, title,
			   node_type, priority, status, progress, weight, created_at, updated_at)
			 VALUES (?, NULL, 0, 1, 'TEST', 'special path node', 'task', 3, 'open', 0, 1,
			   '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')`, id)
		return err
	}))
}

func specialPathNodeCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.readDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM nodes`).Scan(&n))
	return n
}

// TestNew_SpecialCharDir_OpensExactFile: the store opens, persists across a
// reopen and creates nothing beside the directory.
func TestNew_SpecialCharDir_OpensExactFile(t *testing.T) {
	for _, name := range specialDirNames {
		t.Run(name, func(t *testing.T) {
			parent, dir := specialDir(t, name)
			dbPath := filepath.Join(dir, "mtix.db")
			s, err := New(dbPath, slog.Default())
			require.NoError(t, err)
			insertOneNode(t, s, "TEST-1")
			require.NoError(t, s.Close())

			s2, err := New(dbPath, slog.Default())
			require.NoError(t, err)
			assert.Equal(t, 1, specialPathNodeCount(t, s2), "reopen must see the node written to the exact file")
			require.NoError(t, s2.Close())
			requireOnlyEntry(t, parent, name)
		})
	}
}

// TestBackup_SpecialCharDir_VerifiesExactFile: VACUUM INTO and the verify of
// its output both address the exact file, including a single quote.
func TestBackup_SpecialCharDir_VerifiesExactFile(t *testing.T) {
	for _, name := range specialDirNames {
		t.Run(name, func(t *testing.T) {
			s, _ := seedRecoverFixture(t)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			parent, dir := specialDir(t, name)
			dest := filepath.Join(dir, "it's backup.db")

			res, err := s.Backup(context.Background(), dest)
			require.NoError(t, err)
			assert.True(t, res.Verified)
			info, err := os.Stat(dest)
			require.NoError(t, err)
			assert.Positive(t, info.Size())
			ok, err := verifyDatabase(context.Background(), dest)
			require.NoError(t, err)
			assert.True(t, ok)
			requireOnlyEntry(t, parent, name)
		})
	}
}

// TestVerifyDatabase_SpecialCharDirMissing_NeverCreates: verify refuses a
// missing file under such a directory and creates nothing anywhere.
func TestVerifyDatabase_SpecialCharDirMissing_NeverCreates(t *testing.T) {
	for _, name := range specialDirNames {
		t.Run(name, func(t *testing.T) {
			parent, dir := specialDir(t, name)
			ok, err := verifyDatabase(context.Background(), filepath.Join(dir, "gone.db"))
			require.Error(t, err)
			assert.False(t, ok)
			requireOnlyEntry(t, parent, name)
			assert.Empty(t, visibleEntries(t, dir), "the directory must stay empty")
		})
	}
}

// TestRecover_SpecialCharDir_SalvagesExactFile: recover reads the real file
// read-only; no sibling database appears.
func TestRecover_SpecialCharDir_SalvagesExactFile(t *testing.T) {
	for _, name := range specialDirNames {
		t.Run(name, func(t *testing.T) {
			parent, dir := specialDir(t, name)
			dbPath := filepath.Join(dir, "mtix.db")
			s, err := New(dbPath, slog.Default())
			require.NoError(t, err)
			insertOneNode(t, s, "TEST-1")
			require.NoError(t, s.Close())

			res, err := Recover(context.Background(), dbPath, "", "test-version", slog.Default())
			require.NoError(t, err)
			assert.Equal(t, []string{"TEST-1"}, res.RecoveredIDs)
			requireOnlyEntry(t, parent, name)
		})
	}
}

// TestCountUnackedInboxFile_SpecialCharDir_ReadsExactFile: the already-escaped
// reader keeps working once it shares the one DSN helper.
func TestCountUnackedInboxFile_SpecialCharDir_ReadsExactFile(t *testing.T) {
	for _, name := range specialDirNames {
		t.Run(name, func(t *testing.T) {
			parent, dir := specialDir(t, name)
			dbPath := filepath.Join(dir, "mtix.db")
			s, err := New(dbPath, slog.Default())
			require.NoError(t, err)
			require.NoError(t, s.Close())
			n, err := CountUnackedInboxFile(context.Background(), dbPath)
			require.NoError(t, err)
			assert.Zero(t, n)
			requireOnlyEntry(t, parent, name)
		})
	}
}

// TestOpenDB_ReadOnlyMissingPath_NeverCreates: a read-only open of a missing
// file fails and leaves no file, on a plain path and under a special one.
func TestOpenDB_ReadOnlyMissingPath_NeverCreates(t *testing.T) {
	for _, name := range append([]string{"plain"}, specialDirNames...) {
		t.Run(name, func(t *testing.T) {
			parent, dir := specialDir(t, name)
			db, err := openDBWith(context.Background(), filepath.Join(dir, "gone.db"),
				false, true, defaultOpenRetry())
			if err == nil {
				_ = db.Close()
			}
			require.Error(t, err)
			requireOnlyEntry(t, parent, name)
			assert.Empty(t, visibleEntries(t, dir))
		})
	}
}

// TestSpecialDirNamesFor_WindowsDropsQuestionMark pins which rows each host
// runs: only Windows loses the question-mark name.
func TestSpecialDirNamesFor_WindowsDropsQuestionMark(t *testing.T) {
	assert.Equal(t, []string{"c#d", "e%f", "e%3fx", "it's q", "sp ace"}, specialDirNamesFor("windows"))
	assert.Contains(t, specialDirNamesFor("linux"), "a?b")
	assert.Contains(t, specialDirNamesFor("darwin"), "a?b")
	assert.Len(t, specialDirNamesFor("linux"), 6)
}

// TestNew_TempDirPath_WriteReopenReadOnlyVerifyRecover runs the whole path
// round trip on a plain t.TempDir path, which on Windows is a drive path:
// create and write, close, reopen read-only (mode=ro on the file: URI), then
// verify and recover that same file (MTIX-112.2.4).
func TestNew_TempDirPath_WriteReopenReadOnlyVerifyRecover(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mtix.db")

	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	insertOneNode(t, s, "TEST-1")
	require.NoError(t, s.Close())

	ro, err := openDBWith(ctx, dbPath, false, true, defaultOpenRetry())
	require.NoError(t, err)
	var n int
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n))
	assert.Equal(t, 1, n, "the read-only open must see the row written to the exact file")
	_, werr := ro.ExecContext(ctx, `DELETE FROM nodes`)
	require.Error(t, werr, "a read-only open must reject writes")
	require.NoError(t, ro.Close())

	ok, err := verifyDatabase(ctx, dbPath)
	require.NoError(t, err)
	assert.True(t, ok)

	res, err := Recover(ctx, dbPath, "", "test-version", slog.Default())
	require.NoError(t, err)
	assert.Equal(t, []string{"TEST-1"}, res.RecoveredIDs)
	for _, name := range visibleEntries(t, dir) {
		assert.True(t, strings.HasPrefix(name, "mtix.db"), "no sibling database may appear: %s", name)
	}
}
