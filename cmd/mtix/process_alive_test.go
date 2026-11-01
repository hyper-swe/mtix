// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessAliveFromProbe_Outcomes_ClassifiedConservatively pins the
// Windows liveness decision: only an unknown pid and an exited process are
// dead; access denied and unknown errors are alive (MTIX-233).
func TestProcessAliveFromProbe_Outcomes_ClassifiedConservatively(t *testing.T) {
	tests := []struct {
		name      string
		opened    bool
		openErrno uint32
		exitCode  uint32
		want      bool
	}{
		{"opened and still active", true, 0, 259, true},
		{"opened and exited zero", true, 0, 0, false},
		{"opened and exited nonzero", true, 0, 1, false},
		{"opened and exit code 258", true, 0, 258, false},
		{"no such pid", false, 87, 0, false},
		{"access denied", false, 5, 0, true},
		{"unknown error", false, 1234, 0, true},
		{"zero errno without handle", false, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, processAliveFromProbe(tt.opened, tt.openErrno, tt.exitCode))
		})
	}
}

// TestIsProcessAlive_Table_ReportsPerPID covers the live, reaped and absent
// cases through the platform implementation on every OS (MTIX-233).
func TestIsProcessAlive_Table_ReportsPerPID(t *testing.T) {
	tests := []struct {
		name string
		pid  int
		want bool
	}{
		{"current process", os.Getpid(), true},
		{"reaped child", reapedChildPID(t), false},
		{"absent pid", 999999999, false},
		{"negative pid", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isProcessAlive(tt.pid))
		})
	}
}

// TestReadServeRecord_LiveOwnerUnreachable_KeepsRecord proves a record whose
// owner is alive is never removed, even when its port does not answer.
func TestReadServeRecord_LiveOwnerUnreachable_KeepsRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, serveRecordFile)
	content := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"port":` + strconv.Itoa(freeLoopbackPort(t)) + `,"host":"127.0.0.1"}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	_, ok := readServeRecord(dir)

	assert.False(t, ok)
	got, err := os.ReadFile(path) //nolint:gosec // G304: path is under t.TempDir
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
}

// TestReadPIDLock_LiveOwner_KeepsLockAndReportsPort proves the lock of a live
// owner stays on disk after the read.
func TestReadPIDLock_LiveOwner_KeepsLockAndReportsPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, pidLockFile)
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n6849\n"), 0o600))

	port, alive := readPIDLock(dir)

	assert.True(t, alive)
	assert.Equal(t, 6849, port)
	_, err := os.Stat(path)
	assert.NoError(t, err)
}
