// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package segment

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureDir_LinkPlantedAfterMkdir_RefusesAtRecheck verifies FR-21 §5.1:
// a directory replaced by a symlink between Mkdir and the recheck is
// refused with ErrSymlink, and nothing is created behind the link.
func TestEnsureDir_LinkPlantedAfterMkdir_RefusesAtRecheck(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	swap := func(path string, mode os.FileMode) error {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Symlink(outside, path)
	}
	_, err := ensureDirWith(swap, root, "peers", "p1")
	require.ErrorIs(t, err, ErrSymlink)
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "nothing is created at the link target")
}

// TestEnsureDir_ConcurrentCreatorWins_Succeeds verifies that a component
// another process created first (Mkdir reports it exists) is accepted
// when it is a real directory.
func TestEnsureDir_ConcurrentCreatorWins_Succeeds(t *testing.T) {
	root := t.TempDir()
	racer := func(path string, mode os.FileMode) error {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		return &fs.PathError{Op: "mkdir", Path: path, Err: fs.ErrExist}
	}
	got, err := ensureDirWith(racer, root, "peers", "p1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "peers", "p1"), got)
}

// TestEnsureDir_MkdirFails_WrapsError verifies a creation failure other
// than "exists" is returned wrapped with the path.
func TestEnsureDir_MkdirFails_WrapsError(t *testing.T) {
	boom := func(string, os.FileMode) error { return fs.ErrPermission }
	_, err := ensureDirWith(boom, t.TempDir(), "peers")
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Contains(t, err.Error(), "create ")
}
