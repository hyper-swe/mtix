// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Windows API outcomes that decide process liveness (MTIX-233). They are
// plain numbers so the decision below is testable on every OS; only
// process_alive_windows.go calls the Windows API.
const (
	// winErrorInvalidParameter is what OpenProcess reports for a pid that
	// names no process.
	winErrorInvalidParameter uint32 = 87
	// winStillActive is the GetExitCodeProcess value of a running process.
	winStillActive uint32 = 259
)

// processAliveFromProbe turns the outcome of the Windows liveness probe into
// an answer. opened says OpenProcess returned a handle; when it did not,
// openErrno is its error. exitCode is GetExitCodeProcess for an opened handle.
//
// Only two outcomes mean dead: no such pid, and an exited process. Access
// denied and every unknown error mean alive, because the callers delete the
// serve record or PID lock of a process reported dead: a live serve must never
// lose its record on doubt (MTIX-233).
func processAliveFromProbe(opened bool, openErrno, exitCode uint32) bool {
	if !opened {
		return openErrno != winErrorInvalidParameter
	}
	return exitCode == winStillActive
}
