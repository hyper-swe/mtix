// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// NFR-2.8: even a read-only SQLite connection can remove a WAL beside an empty DB.
func TestRecover_EmptyDatabase_PreservesSource(t *testing.T) {
	for _, mode := range []string{"no-mirror", "mirror", "private-export", "symlink", "empty-wal", "corrupt-wal"} {
		t.Run(mode, func(t *testing.T) {
			path, wal, _, _ := crashMailWAL(t)
			if mode == "empty-wal" {
				wal = nil
			}
			if mode == "corrupt-wal" {
				wal[32+24+100] ^= 1
			}
			require.NoError(t, os.WriteFile(path+"-wal", wal, 0600))
			require.NoError(t, os.WriteFile(path, nil, 0600))
			// Do not inspect this damaged fixture through SQLite, even to count rows.
			sourcePath := path
			mirror := filepath.Join(t.TempDir(), "mirror.json")
			options := sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			if mode != "no-mirror" {
				source := newTestStore(t)
				first, _, _ := addressThree(t, source, "recipient")
				require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
				board, err := source.Export(t.Context(), "", "")
				require.NoError(t, err)
				raw, err := json.Marshal(board)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(mirror, raw, 0600))
				if mode == "private-export" {
					options.InboxSources = []*sqlite.ExportData{transportExport(t, source)}
				}
			}
			if mode == "symlink" {
				if runtime.GOOS == "windows" {
					t.Skip("symlinks require Windows privileges")
				}
				path = filepath.Join(t.TempDir(), "alias.db")
				require.NoError(t, os.Symlink(sourcePath, path))
			}
			beforeImage := sourceImage(t, sourcePath)
			result, recoverErr := sqlite.Recover(t.Context(), path, mirror, "test", options)
			for suffix, before := range beforeImage {
				after, err := os.ReadFile(sourcePath + suffix)
				require.NoError(t, err, "source must remain present: %s", suffix)
				require.Equal(t, sha256.Sum256(before), sha256.Sum256(after), "source bytes changed: %s", suffix)
			}
			if mode == "no-mirror" {
				require.Error(t, recoverErr)
				require.Contains(t, recoverErr.Error(), "zero-byte")
				require.Contains(t, recoverErr.Error(), "skipped")
				require.Contains(t, recoverErr.Error(), "no WAL-only salvage")
				require.Contains(t, recoverErr.Error(), "-shm")
				require.Contains(t, recoverErr.Error(), "together")
				require.Contains(t, recoverErr.Error(), "only copy of committed")
				return
			}
			require.NoError(t, recoverErr)
			require.True(t, result.InboxIncomplete)
			require.NotEmpty(t, result.FromMirror)
			require.Contains(t, strings.Join(result.Notes, " "), "zero-byte")
			require.Contains(t, strings.Join(result.Notes, " "), "skipped")
			require.NoError(t, sqlite.ValidateExport(result.Export))
			if mode == "private-export" {
				require.Len(t, result.Export.InboxJournal.Events, 3)
				for _, event := range result.Export.InboxJournal.Events {
					require.False(t, event.Acked)
					require.Empty(t, event.AckedAt)
				}
				dest := newTestStore(t)
				_, err := dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
				mail, err := dest.InboxList(t.Context(), "recipient")
				require.NoError(t, err)
				require.Len(t, mail, 3)
			}
		})
	}
}

func TestCountUnackedInboxFile_EmptyDatabase_PreservesWAL(t *testing.T) {
	path, wal, _, _ := crashMailWAL(t)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	_, err := sqlite.CountUnackedInboxFile(t.Context(), path)
	require.ErrorContains(t, err, "empty or one-byte")
	for suffix, before := range map[string][]byte{"": {}, "-wal": wal} {
		after, err := os.ReadFile(path + suffix)
		require.NoError(t, err)
		require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
	}
}
