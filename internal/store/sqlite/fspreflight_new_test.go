// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// injectFS overrides the filesystem classifier for the duration of a test.
func injectFS(t *testing.T, fsType string, class fsClass) {
	t.Helper()
	orig := fsDetector
	t.Cleanup(func() { fsDetector = orig })
	fsDetector = func(string) (string, fsClass, error) { return fsType, class, nil }
}

func journalMode(t *testing.T, s *Store) string {
	t.Helper()
	var mode string
	require.NoError(t, s.WriteDB().QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode))
	return strings.ToLower(mode)
}

// TestStoreNew_RefusesUnsafeFilesystem: on a FUSE filesystem with no override,
// New refuses to open — the corruption is prevented, not risked (MTIX-54 P0).
func TestStoreNew_RefusesUnsafeFilesystem(t *testing.T) {
	injectFS(t, "macfuse", fsFuse)
	t.Setenv(allowUnsafeFSEnv, "") // not allowed

	_, err := New(t.TempDir(), slog.New(slog.NewTextHandler(logSink{}, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnsafeFilesystem)
	assert.Contains(t, err.Error(), "macfuse")
}

// TestStoreNew_RefusesNetworkFilesystem: same for a network mount.
func TestStoreNew_RefusesNetworkFilesystem(t *testing.T) {
	injectFS(t, "nfs", fsNetwork)
	t.Setenv(allowUnsafeFSEnv, "")

	_, err := New(t.TempDir(), slog.New(slog.NewTextHandler(logSink{}, nil)))
	require.ErrorIs(t, err, errUnsafeFilesystem)
}

// TestStoreNew_UnsafeFS_OverrideOpensNonWAL: with the explicit opt-in, New opens
// but in a non-WAL rollback-journal mode — no -shm, so the FUSE corruption
// vector is removed (MTIX-54 P1).
func TestStoreNew_UnsafeFS_OverrideOpensNonWAL(t *testing.T) {
	injectFS(t, "fuse", fsFuse)
	t.Setenv(allowUnsafeFSEnv, "1")

	s, err := New(t.TempDir(), slog.New(slog.NewTextHandler(logSink{}, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	assert.NotEqual(t, "wal", journalMode(t, s), "unsafe-FS override must NOT use WAL")
}

// TestStoreNew_LocalFilesystem_UsesWAL: on a normal local filesystem, New opens
// in WAL exactly as before — no regression, no false positive.
func TestStoreNew_LocalFilesystem_UsesWAL(t *testing.T) {
	injectFS(t, "apfs", fsLocal)

	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	assert.Equal(t, "wal", journalMode(t, s), "local FS opens in WAL as before")
}

// TestStoreNew_ClassificationError_FailsOpen: a statfs/classify error must not
// block opening — the preflight fails open into normal WAL mode.
func TestStoreNew_ClassificationError_FailsOpen(t *testing.T) {
	orig := fsDetector
	t.Cleanup(func() { fsDetector = orig })
	fsDetector = func(string) (string, fsClass, error) {
		return "", fsLocal, assertErr("statfs boom")
	}

	s, err := New(t.TempDir(), slog.New(slog.NewTextHandler(logSink{}, nil)))
	require.NoError(t, err, "a classify error must not block opening")
	t.Cleanup(func() { _ = s.Close() })
	assert.Equal(t, "wal", journalMode(t, s))
}

// logSink discards log output in tests.
type logSink struct{}

func (logSink) Write(p []byte) (int, error) { return len(p), nil }

type assertErr string

func (e assertErr) Error() string { return string(e) }
