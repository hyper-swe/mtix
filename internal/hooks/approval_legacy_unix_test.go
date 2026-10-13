// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestApprovals_LegacyProvenanceInput_ReturnsWithoutTargetRead(t *testing.T) {
	for _, input := range []string{"tracked", "git file", "index format", "untracked fifo"} {
		t.Run(input, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			root := filepath.Dir(dir)
			prepareLegacyProvenance(t, root, input)
			target := filepath.Join(root, "owned-input")
			require.NoError(t, unix.Mkfifo(target, 0600))
			path := filepath.Join(dir, "hooks.trust")
			if input == "untracked fifo" {
				require.NoError(t, os.Rename(target, path))
				target = path
			} else {
				require.NoError(t, os.Symlink(target, path))
			}
			a := NewApprovals(dir, testutil.OperatorState(t), false)
			require.True(t, awaitLegacyDecision(t, a, target, ConfigHash(dir)))
			decisions, err := a.Check()
			require.NoError(t, err)
			require.False(t, decisions["wake"].Trusted)
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, []byte(ConfigHash(dir)+"\n"), 0600))
			require.NoError(t, a.Migrate())
			decisions, err = a.Check()
			require.NoError(t, err)
			require.False(t, decisions["wake"].Trusted)
		})
	}
}

func prepareLegacyProvenance(t *testing.T, root, input string) {
	t.Helper()
	git := filepath.Join(root, ".git")
	if input == "untracked fifo" {
		return
	}
	if input == "git file" {
		require.NoError(t, os.WriteFile(git, []byte("gitdir: unused\n"), 0600))
		return
	}
	require.NoError(t, os.Mkdir(git, 0700))
	data := approvalIndexFixture(2, ".mtix/hooks.trust", "")
	if input == "index format" {
		data = approvalIndexFixture(5, "", "")
	}
	require.NoError(t, os.WriteFile(filepath.Join(git, "index"), data, 0600))
}

func awaitLegacyDecision(t *testing.T, a *Approvals, target, pin string) bool {
	t.Helper()
	finished := make(chan error, 1)
	go func() { finished <- a.Migrate() }()
	select {
	case err := <-finished:
		require.NoError(t, err)
		return true
	case <-time.After(300 * time.Millisecond):
	}
	// Release this owned fixture and join its reader before reporting the result.
	release, openErr := os.OpenFile(target, os.O_RDWR, 0600)
	var writeErr, closeErr error
	if openErr == nil {
		_, writeErr = release.WriteString(pin)
		closeErr = release.Close()
	}
	select {
	case err := <-finished:
		require.NoError(t, openErr)
		require.NoError(t, writeErr)
		require.NoError(t, closeErr)
		require.NoError(t, err)
		t.Log("owned input released and migration call joined")
	case <-time.After(2 * time.Second):
		t.Fatal("owned migration call did not finish after fixture release")
	}
	return false
}
