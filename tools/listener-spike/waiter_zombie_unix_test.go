// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPidAlive_ZombieProcess_IsNotAlive pins MTIX-254: where nothing reaps
// orphans (a sandbox whose pid 1 never calls wait), a killed process stays a
// zombie and kill(pid, 0) still succeeds. A zombie has exited, so the leak
// check must not count it as a surviving process.
func TestPidAlive_ZombieProcess_IsNotAlive(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() }) // reap; never leave a zombie behind
	require.True(t, pidAlive(pid), "a running process is alive")
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	// Not waited on yet: the child is a zombie until the parent reaps it.
	require.Eventually(t, func() bool { return !pidAlive(pid) }, 5*time.Second, 10*time.Millisecond,
		"an exited but unreaped process must not count as alive")
}
