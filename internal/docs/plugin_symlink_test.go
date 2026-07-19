// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-29 (CWE-59): fileExists used os.Stat, which returns false for a DANGLING
// symlink (target absent). The write-if-absent install paths then created the
// file THROUGH the symlink, at the attacker's target — a committed symlink in a
// shared repo could make `mtix plugin install` write a file OUTSIDE the project
// as the victim. Fix: fileExists uses os.Lstat, so ANY symlink (dangling or not)
// counts as existing → the guard skips → no write-through.

func TestFileExists_DanglingSymlinkCountsAsExisting(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "AGENTS.md")
	target := filepath.Join(dir, "absent-target") // parent exists, target does not
	require.NoError(t, os.Symlink(target, link))

	assert.True(t, fileExists(link),
		"a dangling symlink must count as existing so write-if-absent skips it (CWE-59)")
}

func TestInstallConfigIfAbsent_DoesNotWriteThroughDanglingSymlink(t *testing.T) {
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim.txt") // parent exists, file absent

	installDir := t.TempDir()
	link := filepath.Join(installDir, "config.toml")
	require.NoError(t, os.Symlink(victim, link)) // committed dangling symlink -> victim

	_, err := installConfigIfAbsent(link, "content", "note")
	require.NoError(t, err)

	_, statErr := os.Lstat(victim)
	assert.True(t, os.IsNotExist(statErr),
		"install must NOT create a file through the dangling symlink at the attacker's target")
}

func TestFileExists_RegularFileAndAbsent(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, fileExists(filepath.Join(dir, "nope")), "absent path is not existing")
	realFile := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("x"), 0o600))
	assert.True(t, fileExists(realFile), "a regular file exists")
}
