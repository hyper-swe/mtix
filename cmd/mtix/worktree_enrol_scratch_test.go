// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestWithScratch_StaleDirectoriesFromACrashAreRemoved: a preflight that died
// leaves a full hub copy under enroll.d; the next one clears it first and then
// removes its own scratch directory when done.
func TestWithScratch_StaleDirectoriesFromACrashAreRemoved(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, ".mtix", "enroll.d", "preflight-crashed", "clone")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "mtix.db"), []byte("hub copy"), 0o600))
	var inside string

	require.NoError(t, withScratch(slog.Default(), root, func(scratch string) error {
		inside = scratch
		assert.DirExists(t, scratch)
		return nil
	}))

	left, err := filepath.Glob(filepath.Join(root, ".mtix", "enroll.d", "preflight-*"))
	require.NoError(t, err)
	assert.Empty(t, left, "the stale copy and this run's own directory are both gone")
	assert.NotEmpty(t, inside)
}

func TestWithScratch_FunctionErrorIsKeptAndDirectoryStillRemoved(t *testing.T) {
	root := t.TempDir()

	err := withScratch(slog.Default(), root, func(string) error { return io.ErrUnexpectedEOF })

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	left, _ := filepath.Glob(filepath.Join(root, ".mtix", "enroll.d", "preflight-*"))
	assert.Empty(t, left)
}

func TestWithScratch_EnrolDirIsAFile_IsAnError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "enroll.d"), []byte("x"), 0o600))

	err := withScratch(slog.Default(), root, func(string) error { return nil })

	require.ErrorContains(t, err, "create the enrolment directory")
}

// TestBoundedCloneExport_Failures_AreErrorsNotPasses: a closed local store has
// no cursor or journal to read, and a hub that cannot be read cannot be cloned.
func TestBoundedCloneExport_Failures_AreErrorsNotPasses(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	closed, err := sqlite.New(filepath.Join(t.TempDir(), "d"), slog.Default())
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	_, err = boundedCloneExport(ctx, slog.Default(), root, t.TempDir(), closed, hubOf(nil))
	require.ErrorContains(t, err, "read the")

	live, err := sqlite.New(filepath.Join(t.TempDir(), "live"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = live.Close() })
	failing := hubOf(offlineEvents(t))
	failing.pullErr = io.ErrUnexpectedEOF
	_, err = boundedCloneExport(ctx, slog.Default(), root, t.TempDir(), live, failing)
	require.ErrorContains(t, err, "copy the applied hub events into the scratch clone")

	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))
	_, err = boundedCloneExport(ctx, slog.Default(), root, notADir, live, hubOf(nil))
	require.ErrorContains(t, err, "create the scratch clone directory")
}

func TestPreflightBoard_NeverTouchesTheLiveStoreOrTheBoard(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	before := treeBytes(t, worktreeDataDir(root))
	board := fileBytes(t, boardFilePath(root))

	f, err := preflightBoard(ctx, slog.Default(), root, fakeDial(hubOf(hubEvents)))

	require.NoError(t, err)
	assert.Nil(t, f)
	assert.Equal(t, before, treeBytes(t, worktreeDataDir(root)), "no write touched the live store")
	assert.Equal(t, board, fileBytes(t, boardFilePath(root)))
}

// TestWithScratch_RemovalFailure_IsReported: a scratch directory that cannot be
// removed is an error, not silence, because it holds a full hub copy.
func TestWithScratch_RemovalFailure_IsReported(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not stop removal here")
	}
	root := t.TempDir()
	var stuck string

	err := withScratch(slog.Default(), root, func(scratch string) error {
		stuck = filepath.Join(scratch, "stuck")
		require.NoError(t, os.Mkdir(stuck, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(stuck, "file"), []byte("x"), 0o600))
		require.NoError(t, os.Chmod(stuck, 0o500)) //nolint:gosec // G302: a read-only directory is the point of the test
		return nil
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "stuck")
	require.NoError(t, os.Chmod(stuck, 0o700)) //nolint:gosec // G302: restore so the temp dir is cleaned
}

// TestWithScratch_BracketInTheWorktreePath_StillClearsStaleDirectories: the
// worktree's own path is never read as a glob pattern (review F13). An unclosed
// bracket used to fail every enrolment; a closed one matched nothing and left
// the stale copy.
func TestWithScratch_BracketInTheWorktreePath_StillClearsStaleDirectories(t *testing.T) {
	for _, name := range []string{"wt[", "wt[a]", "wt[ab"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			stale := filepath.Join(root, ".mtix", "enroll.d", "preflight-crashed", "clone")
			require.NoError(t, os.MkdirAll(stale, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(stale, "mtix.db"), []byte("hub copy"), 0o600))
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix", "enroll.d", "keep-me"), 0o700))

			require.NoError(t, withScratch(slog.Default(), root, func(string) error { return nil }))

			assert.NoDirExists(t, filepath.Join(root, ".mtix", "enroll.d", "preflight-crashed"))
			assert.DirExists(t, filepath.Join(root, ".mtix", "enroll.d", "keep-me"), "only preflight- directories are removed")
		})
	}
}
