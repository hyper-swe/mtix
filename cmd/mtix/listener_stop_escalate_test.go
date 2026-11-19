// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freeAfterSignalPolls arms the fake so the lock is released only after n
// sleeps that happen once the signal was sent: a listener that needs a moment
// to exit after SIGTERM. A stop that does not wait after the signal reads it
// as still running (MTIX-276.3.2).
func freeAfterSignalPolls(f *ctlFake, n int) {
	signaled, polls := false, 0
	f.onSignal = func(*ctlFake) { signaled = true }
	f.onSleep = func(f *ctlFake) {
		if !signaled {
			return
		}
		if polls++; polls >= n {
			f.held = false
		}
	}
}

// The listener exits a few polls after SIGTERM, with the signal succeeding or
// failing. The post-signal wait must cover that delay: cutting it to zero
// reports exit 11 for a listener that was about to stop.
func TestListenerStop_LockFreesAfterDelayPostSignal_WaitsAndStops(t *testing.T) {
	tests := []struct {
		name      string
		signalErr error
		wantLine  string
	}{
		{"signal delivered", nil, "sending SIGTERM"},
		{"signal fails", errors.New("signal process 42: os: process already finished"), "could not be delivered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCtlFake()
			f.signalErr = tt.signalErr
			freeAfterSignalPolls(f, 3)
			var out bytes.Buffer
			err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
			require.NoError(t, err)
			assert.Equal(t, []int{42}, f.signals)
			assert.Contains(t, out.String(), tt.wantLine)
			assert.Contains(t, out.String(), "listener stopped")
			assert.NotContains(t, out.String(), "still running after SIGTERM")
			if tt.signalErr == nil {
				assert.NotContains(t, out.String(), "could not be delivered")
			}
		})
	}
}

// The stop request is withdrawn on every outcome of a failed escalation, so a
// listener that survives is not left with a pending stop file.
func TestListenerStop_SignalFailsLockHeld_WithdrawsStopRequest(t *testing.T) {
	f := newCtlFake()
	f.signalErr = errors.New("permission denied")
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
	require.ErrorIs(t, err, errListenerStillRunning)
	assert.Contains(t, out.String(), "could not be delivered")
	assert.Contains(t, out.String(), "listener still running after SIGTERM (pid 42)")
	assert.GreaterOrEqual(t, f.stopClr, 1, "a failed escalation withdraws the stop request")
}

// A lock probe that errors after the failed signal is never read as free:
// the answer is still-running (exit 11), the lock-probe error does not win.
func TestListenerStop_SignalFailsThenLockProbeErrors_ExitsStillRunning(t *testing.T) {
	f := newCtlFake()
	f.signalErr = errors.New("signal process 42: os: process already finished")
	f.onSignal = func(f *ctlFake) { f.held, f.lockErr = false, errors.New("lock probe failed") }
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
	require.ErrorIs(t, err, errListenerStillRunning)
	assert.Equal(t, 11, exitCodeForError(err))
	assert.Contains(t, out.String(), "could not be delivered")
	assert.NotContains(t, out.String(), "listener stopped")
	assert.GreaterOrEqual(t, f.stopClr, 1, "a failed escalation withdraws the stop request")
}

// The post-SIGTERM wait is pinned by literals, not by the production constants,
// so a changed listenerTermWait or listenerPollEvery fails here: 5s of fake
// time in 200ms polls, the last probe exactly 5s after the signal.
const (
	wantTermWait    = 5 * time.Second
	wantPollEvery   = 200 * time.Millisecond
	postSignalPolls = 25
)

// signalClock records the fake clock when the signal is sent, so a test can
// measure how long stop waited after it.
func signalClock(f *ctlFake, freeAtPoll int) (elapsed func() time.Duration) {
	var sentAt time.Time
	signaled, polls := false, 0
	f.onSignal = func(f *ctlFake) { signaled, sentAt = true, f.clock }
	f.onSleep = func(f *ctlFake) {
		if !signaled {
			return
		}
		if polls++; freeAtPoll > 0 && polls >= freeAtPoll {
			f.held = false
		}
	}
	return func() time.Duration { return f.clock.Sub(sentAt) }
}

// With the lock held the whole time, stop waits 5s of fake time
// after the signal, no shorter and no longer, before reporting still running.
// A wait cut to 1s or 2.5s, or stretched to 60s, fails here.
func TestListenerStop_LockHeldAfterSignal_WaitsExactlyTheTermWait(t *testing.T) {
	f := newCtlFake()
	elapsed := signalClock(f, 0)
	var out bytes.Buffer
	err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
	require.ErrorIs(t, err, errListenerStillRunning)
	assert.GreaterOrEqual(t, elapsed(), wantTermWait)
	assert.LessOrEqual(t, elapsed(), wantTermWait+wantPollEvery)
}

// The wait is inclusive of its last probe: a lock freed just before the last
// poll is a success, one freed just after it is exit 11.
func TestListenerStop_LockFreesAtWaitBoundary_SucceedsBeforeFailsAfter(t *testing.T) {
	tests := []struct {
		name       string
		freeAtPoll int
		wantErr    bool
	}{
		{"freed just before the last poll", postSignalPolls, false},
		{"freed just after the last poll", postSignalPolls + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCtlFake()
			signalClock(f, tt.freeAtPoll)
			var out bytes.Buffer
			err := f.ctl().stop(&out, listenerStopOpts{timeout: 2 * time.Second}, false)
			if tt.wantErr {
				require.ErrorIs(t, err, errListenerStillRunning)
				assert.Equal(t, 11, exitCodeForError(err))
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out.String(), "listener stopped")
		})
	}
}
