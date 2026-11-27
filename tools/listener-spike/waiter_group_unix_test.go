// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// pidAlive reports whether a live process with this pid exists. An exited
// but unreaped process (a zombie) is not alive: where nothing reaps orphans
// kill(pid, 0) keeps succeeding for it (MTIX-254).
func pidAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	return !isZombie(pid)
}

// isZombie reports whether the process state is Z. It reads /proc and falls
// back to ps where /proc is absent; an unreadable state counts as not a zombie.
func isZombie(pid int) bool {
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		// The state follows the last ")" of the "pid (comm) S ..." line.
		i := bytes.LastIndexByte(data, ')')
		return i >= 0 && i+2 < len(data) && data[i+2] == 'Z'
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // G204: fixed command, numeric pid
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
