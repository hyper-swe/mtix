// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// platformProcessAlive reports whether a process with the given PID is
// running. os.Process.Signal(0) is unsupported on Windows and always fails,
// so it cannot be used: the process is opened with a query-only right and its
// exit code read instead (MTIX-233). A PID that cannot be queried is reported
// alive, never dead (see processAliveFromProbe).
func platformProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // G115: pid > 0 and a Windows PID fits in 32 bits
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return processAliveFromProbe(false, uint32(errno), 0) //nolint:gosec // G115: Windows error codes fit in 32 bits
		}
		return true
	}
	defer func() {
		// A failed close leaks one handle of a short-lived CLI; the answer holds.
		_ = windows.CloseHandle(handle)
	}()
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return processAliveFromProbe(true, 0, exitCode)
}
