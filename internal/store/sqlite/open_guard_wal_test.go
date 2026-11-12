// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// NFR-2.8: the attach identity read opens SQLite outside Store.New, so it must
// refuse a zero- or one-byte database beside a WAL and leave all three files.
func TestReadStoreInstanceIDFile_ShortDatabaseWithWAL_PreservesSidecars(t *testing.T) {
	for _, size := range []int{0, 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path, _, _, _ := crashMailWAL(t)
			require.NoError(t, os.WriteFile(path, make([]byte, size), 0600))
			before := sourceImage(t, path)
			_, err := sqlite.ReadStoreInstanceIDFile(t.Context(), path)
			require.Error(t, err)
			requireSourceImage(t, path, before, true)
		})
	}
}

// A dangling database link resolves to its target for the WAL check: SQLite
// would create the target empty and unlink the WAL beside it.
func TestNew_DanglingDatabaseSymlinkWithWAL_RefusesAndPreservesWAL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	for _, where := range []string{"beside-target", "beside-link"} {
		t.Run(where, func(t *testing.T) {
			path, _, _, _ := crashMailWAL(t)
			before := sourceImage(t, path)
			dir := t.TempDir()
			link := filepath.Join(dir, "link.db")
			target := filepath.Join(dir, "real.db")
			require.NoError(t, os.Symlink(target, link))
			wal := target + "-wal"
			if where == "beside-link" {
				wal = link + "-wal"
			}
			require.NoError(t, os.WriteFile(wal, before["-wal"], 0600))
			store, err := sqlite.New(link, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if store != nil {
				require.NoError(t, store.Close())
			}
			require.ErrorIs(t, err, model.ErrCorrupted)
			_, statErr := os.Stat(target)
			require.True(t, os.IsNotExist(statErr), "must not create the link target")
			got, readErr := os.ReadFile(wal)
			require.NoError(t, readErr)
			require.Equal(t, before["-wal"], got)
		})
	}
}

// The refusal says why, and truthfully what a copy can and cannot do.
func TestNew_EmptyDatabaseWithWAL_RefusalExplainsReasonAndRestore(t *testing.T) {
	path, _, _, _ := crashMailWAL(t)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	_, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.ErrorIs(t, err, model.ErrCorrupted)
	for _, want := range []string{"deletes its WAL", "move all three files", "a copy", "fresh project", "do not move only the database"} {
		require.Contains(t, strings.ToLower(err.Error()), strings.ToLower(want))
	}
}
