// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package segment_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureDir_CreatesMissingComponents verifies FR-21 §5.1: ordinary
// missing directories are still created, with mode 0700.
func TestEnsureDir_CreatesMissingComponents(t *testing.T) {
	root := t.TempDir()
	got, err := segment.EnsureDir(root, "peers", "p1", "segments")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "peers", "p1", "segments"), got)
	for _, rel := range []string{"peers", "peers/p1", "peers/p1/segments"} {
		fi, err := os.Lstat(filepath.Join(root, rel))
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), rel)
	}
	_, err = segment.EnsureDir(root, "peers", "p1", "segments")
	require.NoError(t, err, "an existing real chain is accepted")
}

// TestEnsureDir_RefusesBeforeCreating verifies that an existing symlink
// or non-directory is refused and nothing is created behind it.
func TestEnsureDir_RefusesBeforeCreating(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root, outside string)
		want    error
	}{
		{"first component symlink", func(t *testing.T, root, outside string) {
			require.NoError(t, os.Symlink(outside, filepath.Join(root, "a")))
		}, segment.ErrSymlink},
		{"middle component symlink", func(t *testing.T, root, outside string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "a"), 0o700))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, "a", "b")))
		}, segment.ErrSymlink},
		{"last component symlink", func(t *testing.T, root, outside string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o700))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, "a", "b", "c")))
		}, segment.ErrSymlink},
		{"dangling symlink", func(t *testing.T, root, outside string) {
			require.NoError(t, os.Symlink(filepath.Join(outside, "missing"), filepath.Join(root, "a")))
		}, segment.ErrSymlink},
		{"regular file component", func(t *testing.T, root, _ string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o600))
		}, segment.ErrForeignEntry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			tt.prepare(t, root, outside)
			_, err := segment.EnsureDir(root, "a", "b", "c")
			assert.ErrorIs(t, err, tt.want)
			entries, rerr := os.ReadDir(outside)
			require.NoError(t, rerr)
			assert.Empty(t, entries, "the symlink destination must stay untouched")
		})
	}
}

// TestEnsureDir_RejectsEscapingComponents verifies that a component
// carrying a separator or dot-dot cannot leave the root.
func TestEnsureDir_RejectsEscapingComponents(t *testing.T) {
	for _, part := range []string{"", ".", "..", "a/b", `a\b`} {
		_, err := segment.EnsureDir(t.TempDir(), part)
		assert.Error(t, err, "component %q", part)
	}
}
