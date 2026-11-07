// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"os"
	"path/filepath"
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
