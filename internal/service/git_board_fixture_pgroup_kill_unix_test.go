// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoardGitFixture_HungGit_KillsHelperProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	// The helper is a background child of the stub, in the stub's process group.
	stub := writeStubGit(t, "sleep 30 &\necho $! > "+pidFile+"\nwait")

	_, err := runBoardGit(stub, t.TempDir(), t.TempDir(), 500*time.Millisecond, "clone")
	require.Error(t, err)

	raw, rerr := os.ReadFile(pidFile) //nolint:gosec // path is the test's own temp file
	require.NoError(t, rerr, "the stub must have started its helper")
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, perr)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) }) // never leak the helper if the assertion fails

	deadline := time.Now().Add(2 * time.Second)
	for {
		kerr := syscall.Kill(pid, 0)
		if errors.Is(kerr, syscall.ESRCH) {
			return
		}
		require.True(t, time.Now().Before(deadline), "helper process %d outlived the killed Git call", pid)
		time.Sleep(20 * time.Millisecond)
	}
}
