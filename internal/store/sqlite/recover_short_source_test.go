// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func shortSourceMirror(t *testing.T) string {
	t.Helper()
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	board, err := source.Export(t.Context(), "", "")
	require.NoError(t, err)
	data, err := json.Marshal(board)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "mirror.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path
}

func sourceImage(t *testing.T, path string) map[string][]byte {
	t.Helper()
	image := make(map[string][]byte)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		require.NoError(t, err)
		image[suffix] = data
	}
	return image
}

func requireSourceImage(t *testing.T, path string, image map[string][]byte, shm bool) {
	t.Helper()
	for suffix, before := range image {
		if suffix == "-shm" && !shm {
			continue
		}
		after, err := os.ReadFile(path + suffix)
		require.NoError(t, err, "preserve source %s", suffix)
		require.Equal(t, sha256.Sum256(before), sha256.Sum256(after), "preserve source %s", suffix)
	}
}

// SQLite treats both zero and one byte as empty; two bytes must take the normal path.
func TestRecover_ShortSourceBoundary(t *testing.T) {
	for _, data := range [][]byte{{}, {0}, {'S'}, {0, 0}, {'S', 'Q'}} {
		t.Run(fmt.Sprintf("%x", data), func(t *testing.T) {
			path, _, _, _ := crashMailWAL(t)
			require.NoError(t, os.WriteFile(path, data, 0600))
			before := sourceImage(t, path)
			result, err := sqlite.Recover(t.Context(), path, shortSourceMirror(t), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			requireSourceImage(t, path, before, len(data) <= 1)
			require.NoError(t, err)
			notes := strings.Join(result.Notes, " ")
			if len(data) <= 1 {
				require.True(t, result.InboxIncomplete)
				require.Contains(t, notes, "skipped")
				require.Contains(t, notes, "no WAL-only salvage")
				require.Contains(t, notes, "-shm")
				require.Contains(t, notes, "together")
				require.Contains(t, notes, "only copy of committed")
			} else {
				require.NotContains(t, notes, "database skipped")
				require.NotContains(t, notes, "no WAL-only salvage")
			}
		})
	}
}

func TestCountUnackedInboxFile_ShortSourceBoundary(t *testing.T) {
	for _, size := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path, _, _, _ := crashMailWAL(t)
			require.NoError(t, os.WriteFile(path, make([]byte, size), 0600))
			before := sourceImage(t, path)
			_, err := sqlite.CountUnackedInboxFile(t.Context(), path)
			require.Error(t, err)
			requireSourceImage(t, path, before, size <= 1)
			if size <= 1 {
				require.Contains(t, err.Error(), "skipped")
			}
		})
	}
}

func TestNew_EmptyDatabaseWithWAL_RefusesWithoutTouchingSource(t *testing.T) {
	for _, size := range []int{0, 1, 2} {
		for _, bypass := range []string{"0", "1"} {
			t.Run(fmt.Sprintf("size%d/bypass%s", size, bypass), func(t *testing.T) {
				t.Setenv("MTIX_SKIP_INTEGRITY_CHECK", bypass)
				path, _, _, _ := crashMailWAL(t)
				require.NoError(t, os.WriteFile(path, make([]byte, size), 0600))
				before := sourceImage(t, path)
				store, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if store != nil {
					require.NoError(t, store.Close())
				}
				requireSourceImage(t, path, before, size <= 1)
				require.Error(t, err)
				if size <= 1 {
					require.ErrorIs(t, err, model.ErrCorrupted)
					require.Contains(t, err.Error(), path+"-wal")
					require.Contains(t, err.Error(), "-shm")
					require.Contains(t, err.Error(), "mtix recover")
					require.Contains(t, err.Error(), "together")
				}
			})
		}
	}
}

type growSourceOnWarning struct {
	slog.Handler
	grow func()
}

func (h growSourceOnWarning) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "recover: database unusable, falling back to mirror" {
		h.grow()
	}
	return h.Handler.Handle(ctx, record)
}

func TestRecover_SourceGrowsAfterBoardSkip_IsIncomplete(t *testing.T) {
	path, _, _, _ := crashMailWAL(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	grown := false
	logger := slog.New(growSourceOnWarning{Handler: slog.NewTextHandler(io.Discard, nil), grow: func() {
		require.NoError(t, os.WriteFile(path, data, 0600))
		grown = true
	}})
	result, err := sqlite.Recover(t.Context(), path, shortSourceMirror(t), "test", sqlite.RecoverOptions{Logger: logger})
	require.NoError(t, err)
	require.True(t, grown)
	require.True(t, result.InboxIncomplete)
	notes := strings.Join(result.Notes, " ")
	require.Contains(t, notes, "size changed")
	require.NotContains(t, notes, "acknowledgment coverage retained")
	require.NotNil(t, result.Export.InboxJournal)
	for _, event := range result.Export.InboxJournal.Events {
		require.False(t, event.Acked)
		require.Empty(t, event.AckedAt)
	}
}

func TestNew_FreshDatabaseWithoutWAL_StillOpens(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh.db")
			if existing {
				require.NoError(t, os.WriteFile(path, nil, 0600))
			}
			store, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)
			require.NoError(t, store.Close())
		})
	}
}
