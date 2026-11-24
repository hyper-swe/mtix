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

// TestEnsureDir_MissingRootAndAncestors_AreCreated verifies that a relay
// root that does not exist yet, with missing ancestors above it, is made
// as os.MkdirAll did before MTIX-305, at mode 0700 (FR-21 §5.1).
func TestEnsureDir_MissingRootAndAncestors_AreCreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "relay")
	got, err := segment.EnsureDir(root, "bootstrap")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "bootstrap"), got)
	for _, dir := range []string{root, got} {
		fi, lerr := os.Lstat(dir)
		require.NoError(t, lerr)
		assert.True(t, fi.IsDir())
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), dir)
	}
}

// TestEnsureDir_SymlinkedRoot_IsFollowed verifies that a root that is
// itself a symlink to a real directory is followed, as before MTIX-305
// (a relay root may be a link to a mounted drive); only the components
// under it are Lstat-checked.
func TestEnsureDir_SymlinkedRoot_IsFollowed(t *testing.T) {
	base, target := t.TempDir(), t.TempDir()
	root := filepath.Join(base, "relay")
	require.NoError(t, os.Symlink(target, root))
	got, err := segment.EnsureDir(root, "bootstrap")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "bootstrap"), got)
	fi, lerr := os.Lstat(filepath.Join(target, "bootstrap"))
	require.NoError(t, lerr)
	assert.True(t, fi.IsDir())
}
