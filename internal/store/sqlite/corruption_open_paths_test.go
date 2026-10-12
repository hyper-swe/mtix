// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// newClosedStoreFile creates a store file and closes it cleanly (MTIX-147).
func newClosedStoreFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := New(path, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	return path
}

// patchFile writes b at off in the file at path.
func patchFile(t *testing.T, path string, off int64, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // test-owned temp file
	require.NoError(t, err)
	_, err = f.WriteAt(b, off)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// forceReadOnlyOpen drops a libfuse orphan beside the file so New takes the
// read-only (unsafe filesystem) path.
func forceReadOnlyOpen(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), ".fuse_hidden0001"), []byte("x"), 0o600))
}

// TestNew_CorruptionOnEveryOpenPath_ReportsCorrupted: a file that cannot be read
// as a database is corruption on EVERY open path (read-only integrity check,
// write-path WAL switch, write-path integrity check, header and truncation
// gates), never "integrity check could not run" (MTIX-147).
func TestNew_CorruptionOnEveryOpenPath_ReportsCorrupted(t *testing.T) {
	damages := []struct {
		name  string
		apply func(t *testing.T, path string)
	}{
		{"invalid page size in header (NOTADB)", func(t *testing.T, p string) {
			patchFile(t, p, 16, []byte{0x03, 0x00})
		}},
		{"garbled page 1 body (CORRUPT)", func(t *testing.T, p string) {
			patchFile(t, p, 100, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
		}},
		{"reserved space byte (CORRUPT)", func(t *testing.T, p string) {
			patchFile(t, p, 20, []byte{200})
		}},
		{"truncated file", func(t *testing.T, p string) {
			info, err := os.Stat(p)
			require.NoError(t, err)
			require.NoError(t, os.Truncate(p, info.Size()/2))
		}},
	}
	for _, readOnly := range []bool{true, false} {
		for _, d := range damages {
			name := d.name + map[bool]string{true: "/read-only", false: "/write"}[readOnly]
			t.Run(name, func(t *testing.T) {
				path := newClosedStoreFile(t)
				d.apply(t, path)
				if readOnly {
					forceReadOnlyOpen(t, path)
				}
				_, err := New(path, slog.New(slog.DiscardHandler))
				require.Error(t, err)
				assert.ErrorIs(t, err, model.ErrCorrupted)
				assert.NotErrorIs(t, err, model.ErrIntegrityCheckIncomplete)
				assert.NotContains(t, err.Error(), "could not run")
			})
		}
	}
}

func TestIsCorruptionError_NotADatabaseAndMalformed_AreCorruption(t *testing.T) {
	for _, msg := range []string{"file is not a database (26)", "database disk image is malformed (11)",
		"enable WAL: file is not a database (26)"} {
		assert.True(t, isCorruptionError(opaqueFromText(msg)), msg)
	}
	assert.False(t, isCorruptionError(opaqueFromText("database is locked (5) (SQLITE_BUSY)")))
}

// opaqueFromText builds an error with only a message, as a layer-crossing error has.
func opaqueFromText(msg string) error { return &textErr{msg} }

type textErr struct{ msg string }

func (e *textErr) Error() string { return e.msg }

// TestNewWithRetry_ReadOnlyPathIntegrityCheck_UsesInjectedRetry: the read-only
// open's integrity check runs under the injected policy, not the default one.
func TestNewWithRetry_ReadOnlyPathIntegrityCheck_UsesInjectedRetry(t *testing.T) {
	path := newClosedStoreFile(t)
	forceReadOnlyOpen(t, path)
	rt, slept := recordingRetry(4)
	calls := 0
	rt.inject = failFirst(stageIntegrity, 2, &calls)
	s, err := newWithRetry(path, slog.New(slog.DiscardHandler), rt)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.Equal(t, 3, calls)
	assert.Len(t, *slept, 2)
}
