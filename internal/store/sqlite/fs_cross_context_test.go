// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-57: cross-context detection. MTIX-54's statfs classifies the LOCAL
// filesystem, but a host directory can look local while a sandbox mounts it over
// FUSE (the host-side blind spot behind the recurring corruption). libfuse leaves
// `.fuse_hidden*` orphans in the directory when it unlinks an open file — their
// presence proves another execution context is accessing this DB via FUSE. Treat
// that as unsafe and refuse writes, even when the FS type reports local.

func TestPreflight_FuseHiddenOrphans_RefuseWrites(t *testing.T) {
	dir := t.TempDir()
	initSafeStore(t, dir) // schema on a local FS

	// A libfuse orphan in the DB dir: another context is FUSE-mounting this dir.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".fuse_hidden0000000100000001"), []byte("x"), 0o600))
	injectFS(t, "apfs", fsLocal) // the FS TYPE looks local — the blind spot

	s, err := New(dir, quietLogger())
	require.NoError(t, err, "still opens read-only for recovery/query")
	t.Cleanup(func() { _ = s.Close() })

	werr := s.WithTx(context.Background(), func(_ *sql.Tx) error { return nil })
	require.ErrorIs(t, werr, errUnsafeFilesystem,
		"cross-context FUSE (.fuse_hidden orphans) must refuse writes even when the FS type is local")
	assert.Contains(t, werr.Error(), "cross-context", "the refusal names the cross-context cause")
}

func TestPreflight_NoFuseHidden_LocalWritesOK(t *testing.T) {
	injectFS(t, "apfs", fsLocal)
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(context.Background(),
			"INSERT INTO meta(key,value) VALUES('t','1') ON CONFLICT(key) DO UPDATE SET value='1'")
		return e
	}), "a clean local dir must not false-positive")
}

// TestDetectCrossContextFUSE: the pure detector.
func TestDetectCrossContextFUSE(t *testing.T) {
	dir := t.TempDir()
	n, detected := detectCrossContextFUSE(dir)
	assert.False(t, detected)
	assert.Equal(t, 0, n)

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".fuse_hidden000000AA00000001"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".fuse_hidden000000AB00000002"), []byte("x"), 0o600))
	n, detected = detectCrossContextFUSE(dir)
	assert.True(t, detected)
	assert.Equal(t, 2, n)
}
