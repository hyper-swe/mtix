//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Native filesystem refusal must not turn unavailable tracked instructions into
// an empty successful selection or read an unverified repository input.
package hooks

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func denyInstructionFixtureRead(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(path, info.Mode().Perm())) })
}

func TestInstructionAgents_UnreadableMetadataReturnsErrorUntilRestored(t *testing.T) {
	for _, kind := range []string{"git_directory", "linked_git_directory", "worktree_pointer", "skills_directory"} {
		t.Run(kind, func(t *testing.T) {
			root, git := instructionRelativeWorktree(t)
			blocked := git
			if kind == "git_directory" {
				root = instructionCoverageRoot(t, "AGENTS.md")
				blocked = filepath.Join(root, ".git")
			}
			if kind == "worktree_pointer" {
				blocked = filepath.Join(root, ".git")
			}
			if kind == "skills_directory" {
				blocked = filepath.Join(root, ".agents", "skills")
				require.NoError(t, os.MkdirAll(blocked, 0700))
			}
			denyInstructionFixtureRead(t, blocked)
			ids, err := InstructionAgents(root, nil)
			require.Error(t, err)
			require.Empty(t, ids)
		})
	}
}

func TestInstructionAgents_NonDirectoryGitMetadataIsRejectedWithoutOpeningPipe(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, ".git"), 0600))
	ids, err := awaitInstructionPipeRefusal(t, root)
	require.ErrorContains(t, err, "not verified")
	require.Empty(t, ids)
}

func TestInstructionWorktreeGit_MissingPointerParentIsAnError(t *testing.T) {
	_, err := instructionWorktreeGit(filepath.Join(t.TempDir(), "missing", ".git"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestInstructionPaths_FileAsRootNeverReturnsPartialSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(path, []byte("synthetic fixture"), 0600))
	paths, err := instructionPaths(path)
	require.Error(t, err)
	require.Empty(t, paths)
}

// Release and join an accidental FIFO reader before reporting a regression.
func awaitInstructionPipeRefusal(t *testing.T, root string) ([]string, error) {
	t.Helper()
	type result struct {
		ids []string
		err error
	}
	done := make(chan result, 1)
	go func() { ids, err := InstructionAgents(root, nil); done <- result{ids, err} }()
	select {
	case got := <-done:
		return got.ids, got.err
	case <-time.After(300 * time.Millisecond):
	}
	release, openErr := os.OpenFile(filepath.Join(root, ".git"), os.O_RDWR, 0600)
	var writeErr, closeErr error
	if openErr == nil {
		_, writeErr = release.WriteString("unverified synthetic pointer\n")
		closeErr = release.Close()
	}
	select {
	case <-done:
		require.NoError(t, openErr)
		require.NoError(t, writeErr)
		require.NoError(t, closeErr)
		t.Fatal("instruction selection read an unverified pipe; owned reader released and joined")
	case <-time.After(2 * time.Second):
		t.Fatal("owned instruction reader did not join after fixture release")
	}
	return nil, nil
}
