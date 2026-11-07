// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestNew_EmptyDatabaseSymlinkWithWAL_PreservesTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	path, _, _, _ := crashMailWAL(t)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	before := sourceImage(t, path)
	alias := filepath.Join(t.TempDir(), "alias.db")
	require.NoError(t, os.Symlink(path, alias))
	store, err := sqlite.New(alias, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if store != nil {
		require.NoError(t, store.Close())
	}
	requireSourceImage(t, path, before, true)
	require.ErrorIs(t, err, model.ErrCorrupted)
	require.Contains(t, err.Error(), path+"-wal")
	target, err := os.Readlink(alias)
	require.NoError(t, err)
	require.Equal(t, path, target)
}

// Moving just the database must no longer let the next command erase its WAL.
func TestNew_MissingDatabaseWithWAL_PreservesSidecars(t *testing.T) {
	path, _, _, _ := crashMailWAL(t)
	before := sourceImage(t, path)
	require.NoError(t, os.Rename(path, path+".aside"))
	store, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if store != nil {
		require.NoError(t, store.Close())
	}
	require.ErrorIs(t, err, model.ErrCorrupted)
	require.Contains(t, err.Error(), path+"-wal")
	require.Contains(t, err.Error(), "together")
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "must not create a replacement database")
	original, err := os.ReadFile(path + ".aside")
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(before[""]), sha256.Sum256(original))
	delete(before, "")
	requireSourceImage(t, path, before, true)
}

func TestNew_EmptyWALDoesNotBlockFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	require.NoError(t, os.WriteFile(path+"-wal", nil, 0600))
	store, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, store.Close())
}
