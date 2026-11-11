// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// startInGroup starts cmd as the leader of its own process group and
// registers a cleanup that kills the whole group and reaps the leader, so
// no waiter or wrapped command outlives the test on any failure path.
func startInGroup(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("kill process group %d: %v", pid, err)
		}
		_ = cmd.Wait() // already reaped by the test on the happy path
	})
}

// pidAlive reports whether a process with this pid exists.
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
