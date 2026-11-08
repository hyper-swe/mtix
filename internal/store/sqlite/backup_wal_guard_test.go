// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// NFR-2.8: VACUUM INTO opens the destination as SQLite, which would reset an
// empty or one-byte destination and delete the WAL beside it. Backup must
// refuse first and leave the destination, its WAL and its shm as they were.
func TestBackup_ShortDestinationBesideWAL_RefusesAndPreservesSidecars(t *testing.T) {
	skipWithoutSymlinks(t)
	for _, where := range []string{"empty", "one-byte", "dangling-link-target", "dangling-link-itself"} {
		t.Run(where, func(t *testing.T) {
			s := newFreshTestStore(t)
			dir := t.TempDir()
			dest := filepath.Join(dir, "backup.db")
			holder := dest
			switch where {
			case "empty":
				require.NoError(t, os.WriteFile(dest, nil, 0o600))
			case "one-byte":
				require.NoError(t, os.WriteFile(dest, []byte{0}, 0o600))
			case "dangling-link-target":
				holder = filepath.Join(dir, "real.db")
				require.NoError(t, os.Symlink(holder, dest))
			case "dangling-link-itself":
				require.NoError(t, os.Symlink(filepath.Join(dir, "real.db"), dest))
			}
			require.NoError(t, os.WriteFile(holder+"-wal", []byte("committed only here"), 0o600))
			require.NoError(t, os.WriteFile(holder+"-shm", []byte("shm"), 0o600))
			_, err := s.Backup(t.Context(), dest)
			require.ErrorIs(t, err, model.ErrCorrupted)
			wal, readErr := os.ReadFile(holder + "-wal")
			require.NoError(t, readErr)
			require.Equal(t, "committed only here", string(wal))
			shm, readErr := os.ReadFile(holder + "-shm")
			require.NoError(t, readErr)
			require.Equal(t, "shm", string(shm))
			_, statErr := os.Stat(filepath.Join(dir, "real.db"))
			require.True(t, os.IsNotExist(statErr), "must not create the link target")
		})
	}
}

// A fresh destination (no WAL beside it) is the normal backup and must work.
func TestBackup_FreshDestination_IsCreated(t *testing.T) {
	s := newFreshTestStore(t)
	res, err := s.Backup(t.Context(), filepath.Join(t.TempDir(), "backup.db"))
	require.NoError(t, err)
	require.True(t, res.Verified)
}

// NFR-2.8: the backup destination refusal is about the destination, not the
// live store. It must say the live store is healthy, point at another
// destination or at moving the stale file with its WAL aside, and must not
// send the operator to recover.
func TestBackup_ShortDestinationRefusal_NamesDestinationNotRecovery(t *testing.T) {
	s := newFreshTestStore(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "backup.db")
	require.NoError(t, os.WriteFile(dest, nil, 0o600))
	require.NoError(t, os.WriteFile(dest+"-wal", []byte("stale"), 0o600))
	_, err := s.Backup(t.Context(), dest)
	require.ErrorIs(t, err, model.ErrCorrupted)
	msg := err.Error()
	require.Contains(t, msg, "choose another destination")
	require.Contains(t, msg, "live store is healthy")
	require.Contains(t, msg, "-wal and -shm together")
	require.False(t, strings.Contains(msg, "mtix recover"), "must not send the operator to recover: %s", msg)
	require.False(t, strings.Contains(msg, "restoring"), "store restore advice does not apply: %s", msg)
}

// NFR-2.8: the destination is opened with SQLITE_OPEN_URI, so SQLite would
// read a destination that starts with file: as a URI (percent decoding, a
// query, file:/// forms) and reach a file the plain-path guard never checked.
// Backup refuses every such destination outright, before touching anything,
// and the files named by the URI stay as they were.
func TestBackup_FileURIDestination_IsRefusedAndNothingIsTouched(t *testing.T) {
	forms := map[string]func(dir string) string{
		"relative":        func(string) string { return "file:e.db" },
		"absolute":        func(dir string) string { return "file:" + dir + "/e.db" },
		"triple-slash":    func(dir string) string { return "file://" + dir + "/e.db" },
		"percent-encoded": func(string) string { return "file:%65.db" },
		"query":           func(string) string { return "file:e.db?cache=private" },
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			s := newFreshTestStore(t)
			dir := t.TempDir()
			t.Chdir(dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "e.db"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "e.db-wal"), []byte("committed only here"), 0o600))
			_, err := s.Backup(t.Context(), form(dir))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.Contains(t, err.Error(), "./file:")
			info, statErr := os.Stat(filepath.Join(dir, "e.db"))
			require.NoError(t, statErr)
			require.Zero(t, info.Size(), "the file the URI names must stay empty")
			wal, readErr := os.ReadFile(filepath.Join(dir, "e.db-wal"))
			require.NoError(t, readErr)
			require.Equal(t, "committed only here", string(wal))
		})
	}
}

// A local file whose name begins with file: is written ./file:x, a plain
// relative path to SQLite, and is still backed up.
func TestBackup_FileNamedDestinationWithDotSlash_IsCreated(t *testing.T) {
	s := newFreshTestStore(t)
	dir := t.TempDir()
	t.Chdir(dir)
	res, err := s.Backup(t.Context(), "./file:backup.db")
	require.NoError(t, err)
	require.True(t, res.Verified)
}
