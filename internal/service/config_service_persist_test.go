// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteFileAtomic_KeepsModeFollowsSymlinkLeavesNoTemp: the config write
// keeps the mode of the file it replaces (0600 stays 0600), writes through a
// symlink without replacing it, and leaves no temp file behind.
func TestWriteFileAtomic_KeepsModeFollowsSymlinkLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	require.NoError(t, os.WriteFile(real, []byte("old"), 0o600))
	link := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(real, link))

	require.NoError(t, writeFileAtomic(link, []byte("new")))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the symlink is kept")
	got, err := os.ReadFile(real)
	require.NoError(t, err)
	require.Equal(t, "new", string(got), "the target is rewritten")
	st, err := os.Stat(real)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "the mode is kept")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "no temp file remains")

	fresh := filepath.Join(dir, "fresh.yaml")
	require.NoError(t, writeFileAtomic(fresh, []byte("x")))
	st, err = os.Stat(fresh)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), st.Mode().Perm(), "a new file gets 0644")
}
