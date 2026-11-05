// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
)

const realGitStoreRel = ".mtix/data/mtix.db"

// gitIn runs real git in dir with a clean, isolated environment.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // fixed git arguments from the test
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// runFix runs a doctor remedy in dir through sh, the way an operator would.
func runFix(t *testing.T, dir, fix string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", fix) //nolint:gosec // the remedy under test
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "fix %q: %s", fix, out)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// committedStoreRepo is a real repository whose store and tasks.json are committed.
func committedStoreRepo(t *testing.T, extra ...string) string {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	gitIn(t, root, append([]string{"init", "-q"}, extra...)...)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix", "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "data", "mtix.db"), []byte("db"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "tasks.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "other.txt"), []byte("o"), 0o644))
	gitIn(t, root, "add", "-f", ".")
	gitIn(t, root, "commit", "-q", "-m", "c")
	return root
}

func TestDoctorGlueRealGit_TrackedStoreFix_ClearsPlainV4AndSplitIndexes(t *testing.T) {
	tests := []struct {
		name  string
		setup []string
	}{
		{"plain index", nil},
		{"index version 4", []string{"update-index", "--index-version", "4"}},
		{"split index", []string{"update-index", "--split-index"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := committedStoreRepo(t)
			if tt.setup != nil {
				gitIn(t, root, tt.setup...)
			}
			kind, err := attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
			require.NoError(t, err)
			require.Equal(t, attach.TrackedStore, kind, "real git wrote an index that tracks the store")

			runFix(t, root, trackedStoreFix)
			kind, err = attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
			require.NoError(t, err)
			assert.Equal(t, attach.TrackedNone, kind, "the printed fix leaves the store untracked")
		})
	}
}

func TestDoctorGlueRealGit_TrackedSymlinkOverStorePath_FixClearsIt(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	elsewhere := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix"), 0o755))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, ".mtix", "data")))
	gitIn(t, root, "add", "-f", ".mtix/data")
	kind, err := attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
	require.NoError(t, err)
	require.Equal(t, attach.TrackedAncestor, kind)

	runFix(t, root, trackedAncestorFix)
	kind, err = attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
	require.NoError(t, err)
	assert.Equal(t, attach.TrackedNone, kind)
}

func TestDoctorGlueRealGit_SparseIndexFix_MakesIndexReadable(t *testing.T) {
	root := committedStoreRepo(t)
	gitIn(t, root, "rm", "-q", "--cached", "-r", ".mtix/data") // the store is not committed; tasks.json is
	gitIn(t, root, "commit", "-q", "-m", "untrack store")
	gitIn(t, root, "sparse-checkout", "init", "--cone", "--sparse-index")
	gitIn(t, root, "sparse-checkout", "set", "other-dir-not-present")

	_, err := attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
	var ie *attach.IndexError
	require.ErrorAs(t, err, &ie, "a sparse directory entry covers .mtix")
	assert.Equal(t, attach.CauseSparse, ie.Cause)

	runFix(t, root, sparseIndexFix)
	kind, err := attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
	require.NoError(t, err)
	assert.Equal(t, attach.TrackedNone, kind)
}

func TestDoctorGlueRealGit_Sha256Repository_IsUnreadableNotUntracked(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "--object-format=sha256")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix", "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "data", "mtix.db"), []byte("db"), 0o644))
	gitIn(t, root, "add", "-f", ".")
	kind, err := attach.ScanStoreInIndex(filepath.Join(root, ".git"), realGitStoreRel)
	require.ErrorIs(t, err, attach.ErrIndexUnsupported, "a SHA-256 index must fail closed, got %q", kind)
}

func TestDoctorGlueRealGit_RelativeWorktree_IsDetected(t *testing.T) {
	requireGit(t)
	root := committedStoreRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, root, "worktree", "add", "-q", "--relative-paths", wt)
	got, err := attach.DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, attach.CheckoutWorktree, got.Kind)
	physical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, physical, got.CommonRoot)
}
