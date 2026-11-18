// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
)

// ctlFake is one injected host: no process, signal or clock is real.
type ctlFake struct {
	goos           string
	held           bool
	lockErr        error
	leaseErr       error
	leaseCalls     int
	leaseErrOnCall int // when > 0, only that (1-based) lease read fails
	pid            int
	proc           procinfo.Proc
	procErr        error
	leases         []oplocal.ListenLease
	signals        []int
	stopSet        int
	stopClr        int
	clock          time.Time
	sleeps         int
	onSleep        func(f *ctlFake)
	onSignal       func(f *ctlFake)
}

func listenerProc(start string) procinfo.Proc {
	return procinfo.Proc{Args: []string{"/usr/bin/mtix", "daemon", "--listen"}, StartTime: start}
}

func newCtlFake() *ctlFake {
	return &ctlFake{goos: "linux", held: true, pid: 42, proc: listenerProc("t1"),
		clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *ctlFake) ctl() *listenerCtl {
	return &listenerCtl{
		goos:       f.goos,
		projectDir: "/work/proj",
		lockHeld:   func() (bool, error) { return f.held, f.lockErr },
		readPID:    func() (int, error) { return f.pid, nil },
		info:       func(int) (procinfo.Proc, error) { return f.proc, f.procErr },
		leases: func() ([]oplocal.ListenLease, error) {
			f.leaseCalls++
			if f.leaseErrOnCall > 0 && f.leaseCalls != f.leaseErrOnCall {
				return f.leases, nil
			}
			return f.leases, f.leaseErr
		},
		signal: func(pid int) error {
			f.signals = append(f.signals, pid)
			if f.onSignal != nil {
				f.onSignal(f)
			}
			return nil
		},
		writeStop: func() error { f.stopSet++; return nil },
		clearStop: func() error { f.stopClr++; return nil },
		now:       func() time.Time { return f.clock },
		sleep: func(d time.Duration) {
			f.sleeps++
			f.clock = f.clock.Add(d)
			if f.onSleep != nil {
				f.onSleep(f)
			}
		},
	}
}

func (f *ctlFake) waiter() {
	f.leases = []oplocal.ListenLease{{PID: 900, Agent: "agent-a", ArmedAt: f.clock}}
}

func TestListenerStatus_Running_ReportsPidAndWaiters(t *testing.T) {
	f := newCtlFake()
	f.waiter()
	var out bytes.Buffer
	require.NoError(t, f.ctl().status(&out, false))
	assert.Contains(t, out.String(), "listener running")
	assert.Contains(t, out.String(), "pid 42")
	assert.Contains(t, out.String(), "agent-a")
	assert.Empty(t, f.signals)
	assert.Zero(t, f.stopSet, "status never writes a stop request")
}

func TestListenerStatus_NoLock_ExitsAsNotRunning(t *testing.T) {
	f := newCtlFake()
	f.held = false
	var out bytes.Buffer
	err := f.ctl().status(&out, false)
	require.ErrorIs(t, err, errListenerNotRunning)
	assert.Contains(t, out.String(), "no listener is running")
}

func TestListenerStatus_NotAListener_IsNotRunningAndPointsAtService(t *testing.T) {
	tests := []struct {
		name string
		proc procinfo.Proc
		want error
		text string
	}{
		{"service daemon", procinfo.Proc{Args: []string{"mtix", "daemon"}}, errListenerDaemonHeld, "mtix daemon stop"},
		{"foreground sync daemon", procinfo.Proc{Args: []string{"mtix", "sync", "daemon"}}, errListenerDaemonHeld, "mtix daemon stop"},
		{"args unreadable", procinfo.Proc{}, errListenerUnverified, "cannot be read"},
		{"other binary", procinfo.Proc{Args: []string{"/bin/sleep", "daemon", "--listen"}}, errListenerUnverified, "not an mtix binary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCtlFake()
			f.proc = tt.proc
			var out bytes.Buffer
			require.ErrorIs(t, f.ctl().status(&out, false), tt.want)
			assert.Contains(t, out.String(), tt.text)
		})
	}
}

func TestListenerStatus_UnsupportedOrMissingPid_IsUnverified(t *testing.T) {
	f := newCtlFake()
	f.procErr = procinfo.ErrUnsupported
	require.ErrorIs(t, f.ctl().status(&bytes.Buffer{}, false), errListenerUnverified)
	f = newCtlFake()
	f.pid = 0
	require.ErrorIs(t, f.ctl().status(&bytes.Buffer{}, false), errListenerUnverified)
}

func TestListenerStatus_JSON_IsMachineReadable(t *testing.T) {
	f := newCtlFake()
	f.waiter()
	var out bytes.Buffer
	require.NoError(t, f.ctl().status(&out, true))
	var got listenerReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "running", got.State)
	assert.Equal(t, 42, got.PID)
	require.Len(t, got.Waiters, 1)
	assert.Equal(t, "agent-a", got.Waiters[0].Agent)
}

func TestListenerStop_Cooperative_WritesRequestWaitsForLockAndSignalsNothing(t *testing.T) {
	f := newCtlFake()
	f.onSleep = func(f *ctlFake) { f.held = f.sleeps < 3 }
	var out bytes.Buffer
	require.NoError(t, f.ctl().stop(&out, listenerStopOpts{timeout: 30 * time.Second}, false))
	assert.Equal(t, 1, f.stopSet)
	assert.GreaterOrEqual(t, f.stopClr, 1, "the request file is cleaned up")
	assert.Empty(t, f.signals)
	assert.Contains(t, out.String(), "listener stopped")
	assert.Contains(t, out.String(), "mtix daemon status --listener")
}

func TestListenerStop_NothingRunning_IsIdempotent(t *testing.T) {
	f := newCtlFake()
	f.held = false
	var out bytes.Buffer
	require.NoError(t, f.ctl().stop(&out, listenerStopOpts{timeout: time.Second}, false))
	assert.Zero(t, f.stopSet)
	assert.Contains(t, out.String(), "no listener is running")
}

func TestListenerStop_ServiceDaemon_RefusesAndWritesNothing(t *testing.T) {
	f := newCtlFake()
	f.proc = procinfo.Proc{Args: []string{"mtix", "daemon"}}
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: time.Second}, false)
	require.ErrorIs(t, err, errListenerDaemonHeld)
	assert.Zero(t, f.stopSet)
	assert.Empty(t, f.signals)
	assert.Contains(t, out.String(), "mtix daemon stop")
}

func TestListenerStop_LiveWaiterRespawns_FailsAndListsWaiter(t *testing.T) {
	f := newCtlFake()
	f.waiter()
	f.onSleep = func(f *ctlFake) {
		if f.sleeps == 1 {
			f.held = false
		}
		if f.sleeps == 2 {
			f.held, f.pid, f.proc = true, 77, listenerProc("t2")
		}
	}
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 30 * time.Second}, false)
	require.ErrorIs(t, err, errListenerRespawned)
	assert.Contains(t, out.String(), "agent-a")
	assert.Empty(t, f.signals)
}

func TestListenerStop_Timeout_EscalatesOnlyForAVerifiedListener(t *testing.T) {
	f := newCtlFake()
	f.onSignal = func(f *ctlFake) { f.held = false }
	var out bytes.Buffer
	require.NoError(t, f.ctl().stop(&out, listenerStopOpts{timeout: 3 * time.Second}, false))
	assert.Equal(t, []int{42}, f.signals)
	assert.Contains(t, out.String(), "listener stopped")
}

func TestListenerStop_Timeout_FailsClosedWithoutASignal(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		mutate func(f *ctlFake)
	}{
		{"windows never signals", "windows", func(*ctlFake) {}},
		{"start time changed", "linux", func(f *ctlFake) {
			f.onSleep = func(f *ctlFake) { f.proc = listenerProc("reused-pid") }
		}},
		{"args changed", "linux", func(f *ctlFake) {
			f.onSleep = func(f *ctlFake) { f.proc = procinfo.Proc{Args: []string{"mtix", "daemon"}, StartTime: "t1"} }
		}},
		{"process gone from view", "linux", func(f *ctlFake) {
			f.onSleep = func(f *ctlFake) { f.procErr = procinfo.ErrNoProcess }
		}},
		{"info unsupported", "linux", func(f *ctlFake) { f.procErr = procinfo.ErrUnsupported }},
		{"pid file missing", "linux", func(f *ctlFake) { f.pid = 0 }},
		{"other directory", "linux", func(f *ctlFake) {
			f.proc = procinfo.Proc{Args: []string{"mtix", "daemon", "--listen"}, StartTime: "t1", Cwd: "/work/other"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCtlFake()
			f.goos = tt.goos
			tt.mutate(f)
			var out bytes.Buffer
			err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
			require.Error(t, err)
			assert.Empty(t, f.signals, "an unverified process is never signaled")
			assert.GreaterOrEqual(t, f.stopClr, 1, "a failed stop withdraws its request")
		})
	}
}

func TestListenerStop_StillHeldAfterSignal_Fails(t *testing.T) {
	f := newCtlFake()
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
	require.Error(t, err)
	assert.Equal(t, []int{42}, f.signals)
	assert.False(t, errors.Is(err, errListenerNotRunning))
	assert.Contains(t, out.String(), "still running")
}

// A waiter-lease read failure must never read as "lock free" (exit 10): the
// relay clone and retire-peer recovery gate treats exit 10 as the only proof
// that nothing runs (MTIX-276.4). The lock is free here on purpose, so a
// swallowed lease error would fall through to the not-running answer.
func TestListenerControl_LeaseReadError_ExitsOneNeverTenOrZero(t *testing.T) {
	leaseFail := errors.New("lease directory unreadable")
	tests := []struct {
		name string
		run  func(c *listenerCtl, out *bytes.Buffer) error
	}{
		{"status", func(c *listenerCtl, out *bytes.Buffer) error { return c.status(out, false) }},
		{"status json", func(c *listenerCtl, out *bytes.Buffer) error { return c.status(out, true) }},
		{"stop", func(c *listenerCtl, out *bytes.Buffer) error {
			return c.stop(out, listenerStopOpts{timeout: time.Second}, false)
		}},
	}
	for _, held := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s held=%t", tt.name, held), func(t *testing.T) {
				f := newCtlFake()
				f.held = held
				f.leaseErr = leaseFail
				var out bytes.Buffer
				err := tt.run(f.ctl(), &out)
				require.ErrorIs(t, err, leaseFail)
				assert.NotErrorIs(t, err, errListenerNotRunning)
				assert.Equal(t, 1, exitCodeForError(err))
				assert.Empty(t, out.String(), "no state is reported on a failed read")
				assert.Zero(t, f.stopSet, "no stop request is written")
				assert.Empty(t, f.signals)
			})
		}
	}
}

// The lease read can also fail on a later call inside one stop: after the
// listener exited (afterStop) and while re-verifying it before a signal
// (escalate). Neither may turn into success or a signal (MTIX-276.4).
func TestListenerStop_LeaseReadErrorAfterStop_ReturnsTheError(t *testing.T) {
	leaseFail := errors.New("lease directory unreadable")
	f := newCtlFake()
	f.leaseErr, f.leaseErrOnCall = leaseFail, 2
	f.onSleep = func(f *ctlFake) { f.held = false }
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 3 * time.Second}, false)
	require.ErrorIs(t, err, leaseFail)
	assert.Equal(t, 1, exitCodeForError(err))
	assert.NotContains(t, out.String(), "listener stopped")
	assert.Empty(t, f.signals)
}

func TestListenerStop_LeaseReadErrorInEscalate_FailsClosedWithoutASignal(t *testing.T) {
	f := newCtlFake()
	f.leaseErr, f.leaseErrOnCall = errors.New("lease directory unreadable"), 2
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errListenerNotRunning)
	assert.Contains(t, out.String(), "could not be re-checked")
	assert.Empty(t, f.signals, "a failed re-check never signals")
	assert.GreaterOrEqual(t, f.stopClr, 1, "a failed stop withdraws its request")
}
