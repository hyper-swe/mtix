// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// leakReportEnv names the file the deliberately failing helper test writes
// the pids it started to; the helper only runs when it is set.
const leakReportEnv = "WAITER_LEAK_REPORT"

// TestWaiterHelper_DeliberateFailure is not a test of its own: it is the
// body TestWaiterGuard_FailingTest_LeavesNoProcessesBehind runs in a child
// test process. It arms two waiters, reports their pids and then fails on
// purpose, the way any red run or timeout would.
func TestWaiterHelper_DeliberateFailure(t *testing.T) {
	report := os.Getenv(leakReportEnv)
	if report == "" {
		t.Skip("helper for TestWaiterGuard_FailingTest_LeavesNoProcessesBehind")
	}
	e := newWaiterEnv(t)
	w0 := e.arm(t, "w0", 0)
	w1 := e.arm(t, "w1", 0)
	// Either arm may win the lock, so the holder is whichever one logged
	// "armed"; its wrapped command is the one that writes a pid file.
	var holder string
	require.Eventually(t, func() bool {
		ev := e.events(t)
		holder = e.armedLabel(t)
		if holder == "" || count(ev, "guard-skip") != 1 {
			return false
		}
		_, err := os.Stat(e.loopPIDFile(holder))
		return err == nil
	}, waiterDeadline(), 10*time.Millisecond)
	loop, err := os.ReadFile(e.loopPIDFile(holder))
	require.NoError(t, err)
	pids := []string{strconv.Itoa(w0.Process.Pid), strconv.Itoa(w1.Process.Pid), strings.TrimSpace(string(loop))}
	// Real file in the report path is written last so the parent only sees
	// complete reports.
	tmp := report + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(strings.Join(pids, "\n")+"\n"), 0o600))
	require.NoError(t, os.Rename(tmp, report))
	require.Fail(t, "deliberate failure before the release file is written")
}

// armedLabel returns the label of the waiter that logged "armed", or "".
func (e waiterEnv) armedLabel(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.log) //nolint:gosec // G304: path under this test's TempDir
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[2] == "armed" {
			return f[1]
		}
	}
	return ""
}

func TestWaiterGuard_FailingTest_LeavesNoProcessesBehind(t *testing.T) {
	newWaiterEnv(t) // skips on windows
	report := filepath.Join(t.TempDir(), "pids")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWaiterHelper_DeliberateFailure$", "-test.count=1",
		"-test.timeout="+(3*waiterDeadline()).String())
	cmd.Env = append(os.Environ(), leakReportEnv+"="+report)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "the helper must fail on purpose:\n%s", out)
	data, rerr := os.ReadFile(report) //nolint:gosec // G304: path under this test's TempDir
	require.NoError(t, rerr, "the helper must have reported its pids:\n%s", out)
	var pids []int
	for _, f := range strings.Fields(string(data)) {
		pid, aerr := strconv.Atoi(f)
		require.NoError(t, aerr)
		pids = append(pids, pid)
	}
	require.Len(t, pids, 3)
	// Never leave a leak behind even when this test fails: kill only the
	// pids the helper reported.
	t.Cleanup(func() {
		for _, pid := range pids {
			if pidAlive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	require.Eventually(t, func() bool {
		for _, pid := range pids {
			if pidAlive(pid) {
				return false
			}
		}
		return true
	}, waiterDeadline(), 20*time.Millisecond, "processes still alive after the failing test: %v", pids)
}

// TestWaiterGuard_SIGTERM_StopsTheWrappedCommandAndReleasesTheLock pins that
// terminating the waiter also terminates the command it wraps.
func TestWaiterGuard_SIGTERM_StopsTheWrappedCommandAndReleasesTheLock(t *testing.T) {
	e := newWaiterEnv(t)
	cmd := e.arm(t, "w", 0)
	require.Eventually(t, func() bool {
		_, err := os.Stat(e.loopPIDFile("w"))
		return count(e.events(t), "armed") == 1 && err == nil
	}, waiterDeadline(), 10*time.Millisecond)
	data, err := os.ReadFile(e.loopPIDFile("w"))
	require.NoError(t, err)
	loop, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		var exitErr *exec.ExitError
		require.ErrorAs(t, werr, &exitErr)
		require.Equal(t, 143, exitErr.ExitCode())
	case <-time.After(waiterDeadline()):
		t.Fatal("the waiter ignored SIGTERM while its wrapped command ran")
	}
	require.Eventually(t, func() bool { return !pidAlive(loop) }, waiterDeadline(), 20*time.Millisecond,
		"the wrapped command must not outlive the waiter")
	_, lerr := os.Lstat(e.lock)
	require.ErrorIs(t, lerr, os.ErrNotExist, "the lock is released on SIGTERM")
}
