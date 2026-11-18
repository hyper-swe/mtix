// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// TestNewDaemonListener_StaleStopRequest_DoesNotEndTheNewListener proves the
// clear at listener start through the constructor the daemon calls: a request
// left by an interrupted stop must not end every future listener at once.
func TestNewDaemonListener_StaleStopRequest_DoesNotEndTheNewListener(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	prev := app.mtixDir
	t.Cleanup(func() { app.mtixDir = prev })
	app.mtixDir = dir
	require.NoError(t, writeListenerStopRequest(dir))
	require.True(t, listenerStopRequested(dir), "precondition: a stale request exists")

	l, err := newDaemonListener(false, 5)
	require.NoError(t, err)
	assert.False(t, l.stopRequested(), "a request that predates the listener is not for it")
	require.NoError(t, writeListenerStopRequest(dir))
	assert.True(t, l.stopRequested(), "a request made after the start still ends it")
}

// TestListenerStop_LockProbeErrorDuringWait_NeverReportsStopped: a failing
// probe is not evidence that the lock is free.
func TestListenerStop_LockProbeErrorDuringWait_NeverReportsStopped(t *testing.T) {
	f := newCtlFake()
	f.onSleep = func(f *ctlFake) { f.lockErr, f.held = errors.New("probe failed"), false }
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
	require.Error(t, err)
	assert.NotContains(t, out.String(), "listener stopped")
	assert.Empty(t, f.signals)
	assert.Equal(t, exitCodeListenerStillRunning, exitCodeForError(err))
}

// TestListenerStop_SignalPathGuards: each re-check before SIGTERM is its own
// guard, so a test changes exactly one thing between the two inspections.
func TestListenerStop_SignalPathGuards_EachCheckAloneRefusesTheSignal(t *testing.T) {
	tests := []struct {
		name   string
		first  procinfo.Proc
		second func(f *ctlFake)
	}{
		{"only the pid changes", listenerProc("t1"), func(f *ctlFake) { f.pid = 77 }},
		{"first inspect unverified, second running", procinfo.Proc{Args: []string{"/bin/sleep", "1"}, StartTime: "t1"},
			func(f *ctlFake) { f.proc = listenerProc("t1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCtlFake()
			f.proc = tt.first
			f.onSleep = tt.second
			var out bytes.Buffer
			err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
			require.ErrorIs(t, err, errListenerStillRunning)
			assert.Empty(t, f.signals, "nothing is signaled when only one check can refuse")
		})
	}
}

// TestExitCodeForError_ListenerCodes_AreDistinctFromGenericErrors pins the
// recovery gate: only exit 10 means no listener runs, and every error that is
// not that stays on another code.
func TestExitCodeForError_ListenerCodes_AreDistinctFromGenericErrors(t *testing.T) {
	assert.Equal(t, 8, exitCodeForError(fmt.Errorf("x: %w", errListenerUnverified)))
	assert.Equal(t, 9, exitCodeForError(fmt.Errorf("x: %w", errListenerRespawned)))
	assert.Equal(t, 10, exitCodeForError(fmt.Errorf("x: %w", errListenerNotRunning)))
	assert.Equal(t, 11, exitCodeForError(fmt.Errorf("x: %w", errListenerStillRunning)))
	assert.Equal(t, 12, exitCodeForError(fmt.Errorf("x: %w", errListenerDaemonHeld)))
	assert.Equal(t, exitCodeGeneric, exitCodeForError(errors.New("mtix daemon: not in an mtix project")))

	f := newCtlFake()
	f.lockErr = errors.New("lock probe failed")
	err := f.ctl().status(&bytes.Buffer{}, false)
	require.Error(t, err)
	assert.Equal(t, exitCodeGeneric, exitCodeForError(err), "a probe error is never read as none")
	f = newCtlFake()
	f.held = false
	assert.Equal(t, exitCodeListenerNone, exitCodeForError(f.ctl().status(&bytes.Buffer{}, false)))
}

// TestListenerStatus_ForegroundDaemonHoldsLock_ExitsDaemonHeldNotNone: a
// foreground daemon (or the service) holding the lock must never read as the
// free-lock exit 10 that the recovery gate requires.
func TestListenerStatus_ForegroundDaemonHoldsLock_ExitsDaemonHeldNotNone(t *testing.T) {
	for _, args := range [][]string{{"mtix", "daemon"}, {"mtix", "sync", "daemon"}} {
		f := newCtlFake()
		f.proc = procinfo.Proc{Args: args, StartTime: "t1"}
		err := f.ctl().status(&bytes.Buffer{}, false)
		require.ErrorIs(t, err, errListenerDaemonHeld)
		assert.Equal(t, exitCodeListenerDaemonHeld, exitCodeForError(err))
		assert.NotEqual(t, exitCodeListenerNone, exitCodeForError(err))
	}
}
