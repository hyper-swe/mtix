// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
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

// boardGitHelperCeiling is the generous ceiling for each observable condition
// the test waits on (helper started, helper gone); it is never slept out.
const boardGitHelperCeiling = 30 * time.Second

// waitBoardHelperPID waits until the stub has published its helper pid.
func waitBoardHelperPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(boardGitHelperCeiling)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile) //nolint:gosec // path is the test's own temp file
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.FailNow(t, "the stub never published its helper pid")
	return 0
}

func TestBoardGitFixture_HungGit_KillsHelperProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "helper.pid")
	// The helper is a background child of the stub, in the stub's process group.
	// The pid is published by rename so a reader never sees a partial file.
	stub := writeStubGit(t, "sleep 120 &\necho $! > "+pidFile+".tmp\nmv "+pidFile+".tmp "+pidFile+"\nwait")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runBoardGitCtx(ctx, stub, t.TempDir(), t.TempDir(), boardGitHelperCeiling, "clone")
		done <- err
	}()

	pid := waitBoardHelperPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) }) // never leak the helper if the assertion fails
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(boardGitHelperCeiling):
		require.FailNow(t, "the cancelled Git call never returned")
	}

	deadline := time.Now().Add(boardGitHelperCeiling)
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		require.True(t, time.Now().Before(deadline), "helper process %d outlived the killed Git call", pid)
		time.Sleep(20 * time.Millisecond)
	}
}
