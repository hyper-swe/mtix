// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingManager struct {
	calls int
	err   error
}

func (m *countingManager) Restart(context.Context) error {
	m.calls++
	return m.err
}

var restartNow = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

func olderDispatcher(marked bool) RestartRequest {
	return RestartRequest{CLIProtocol: 2, DispatcherPID: 4242, DispatcherProtocol: 1, HarnessMarked: marked}
}

func TestCLIRestartsDispatcher_UnderHarnessMarker_Never(t *testing.T) {
	mgr := &countingManager{}
	st := newMemoryStore(t.TempDir())
	r := NewRestarter(mgr, st, func() time.Time { return restartNow })
	outcome, err := r.MaybeRestart(context.Background(), olderDispatcher(true))
	require.NoError(t, err)
	assert.Equal(t, OutcomeHarnessMarker, outcome)
	assert.Zero(t, mgr.calls, "a CLI under a harness session marker never gains OS-service control")
	assert.Empty(t, st.records, "nothing recorded, so the next unmarked CLI may still restart")
}

func TestCLIRestartsDispatcher_AtMostHourly(t *testing.T) {
	mgr := &countingManager{}
	st := newMemoryStore(t.TempDir())
	now := restartNow
	r := NewRestarter(mgr, st, func() time.Time { return now })

	outcome, err := r.MaybeRestart(context.Background(), olderDispatcher(false))
	require.NoError(t, err)
	assert.Equal(t, OutcomeRestarted, outcome)

	now = restartNow.Add(59 * time.Minute)
	outcome, err = r.MaybeRestart(context.Background(), olderDispatcher(false))
	require.NoError(t, err)
	assert.Equal(t, OutcomeRateLimited, outcome)
	assert.Equal(t, 1, mgr.calls)

	now = restartNow.Add(time.Hour)
	outcome, err = r.MaybeRestart(context.Background(), olderDispatcher(false))
	require.NoError(t, err)
	assert.Equal(t, OutcomeRestarted, outcome)
	assert.Equal(t, 2, mgr.calls)
}

func TestCLIRestartsDispatcher_Outcomes(t *testing.T) {
	unreachable := errors.New("service manager socket refused")
	tests := []struct {
		name     string
		mgr      ServiceManager
		req      RestartRequest
		writeErr error
		want     Outcome
		wantErr  error
		wantCall bool
	}{
		{"current dispatcher left alone", &countingManager{}, RestartRequest{CLIProtocol: 2, DispatcherPID: 1, DispatcherProtocol: 2}, nil, OutcomeCurrent, nil, false},
		{"newer dispatcher left alone", &countingManager{}, RestartRequest{CLIProtocol: 2, DispatcherPID: 1, DispatcherProtocol: 3}, nil, OutcomeCurrent, nil, false},
		{"no dispatcher service installed says so", NoServiceManager{}, olderDispatcher(false), nil, OutcomeNoService, nil, false},
		{"unreachable service manager", &countingManager{err: unreachable}, olderDispatcher(false), nil, OutcomeUnreachable, unreachable, true},
		{"unwritable operator state never restarts", &countingManager{}, olderDispatcher(false), errSandboxed, OutcomeStateUnwritable, errSandboxed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newMemoryStore(t.TempDir())
			st.writeErr = tt.writeErr
			outcome, err := NewRestarter(tt.mgr, st, func() time.Time { return restartNow }).MaybeRestart(context.Background(), tt.req)
			assert.Equal(t, tt.want, outcome)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			if m, ok := tt.mgr.(*countingManager); ok {
				assert.Equal(t, tt.wantCall, m.calls == 1)
			}
			assert.NotEqual(t, OutcomeRestarted, outcome, "only a real restart reports success")
		})
	}
}

func TestNoServiceManager_Restart_NamesMissingService(t *testing.T) {
	err := NoServiceManager{}.Restart(context.Background())
	require.ErrorIs(t, err, ErrNoDispatcherService)
	assert.Contains(t, err.Error(), "no dispatcher service is installed")
}

func TestDecideSelfRestart_OnlyUnderRestartingSupervisor(t *testing.T) {
	tests := []struct {
		name      string
		in        SelfRestartInput
		wantExit  bool
		wantAlert bool
	}{
		{"launchd KeepAlive exits at idle", SelfRestartInput{Supervisor: SupervisorKeepAlive, TargetChanged: true, TargetValid: true}, true, false},
		{"systemd Restart=always exits at idle", SelfRestartInput{Supervisor: SupervisorRestartAlways, TargetChanged: true, TargetValid: true}, true, false},
		{"Task Scheduler keeps running and alerts", SelfRestartInput{Supervisor: SupervisorBestEffort, TargetChanged: true, TargetValid: true}, false, true},
		{"foreground keeps running and alerts", SelfRestartInput{Supervisor: SupervisorForeground, TargetChanged: true, TargetValid: true}, false, true},
		{"unchanged target does nothing", SelfRestartInput{Supervisor: SupervisorKeepAlive}, false, false},
		{"refused new target never exits", SelfRestartInput{Supervisor: SupervisorKeepAlive, TargetChanged: true}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideSelfRestart(tt.in)
			assert.Equal(t, tt.wantExit, got.Exit)
			assert.Equal(t, tt.wantAlert, got.Alert)
			if got.Alert {
				assert.NotEmpty(t, got.Reason)
			}
		})
	}
}

func TestSupervisorRestartsOnExit_OnlyKeepAliveAndRestartAlways(t *testing.T) {
	assert.True(t, SupervisorKeepAlive.RestartsOnExit())
	assert.True(t, SupervisorRestartAlways.RestartsOnExit())
	assert.False(t, SupervisorBestEffort.RestartsOnExit())
	assert.False(t, SupervisorForeground.RestartsOnExit())
}

func TestCLIRestartsDispatcher_NoService_SecondAttemptStillSaysNoService(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	now := restartNow
	r := NewRestarter(NoServiceManager{}, st, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		outcome, err := r.MaybeRestart(context.Background(), olderDispatcher(false))
		require.NoError(t, err)
		assert.Equal(t, OutcomeNoService, outcome, "attempt %d", i+1)
		now = now.Add(10 * time.Minute)
	}
}

func TestCLIRestartsDispatcher_FailedAttempt_DoesNotThrottleNextRestart(t *testing.T) {
	st := newMemoryStore(t.TempDir())
	mgr := &countingManager{err: errors.New("service manager busy")}
	now := restartNow
	r := NewRestarter(mgr, st, func() time.Time { return now })
	outcome, err := r.MaybeRestart(context.Background(), olderDispatcher(false))
	require.Error(t, err)
	assert.Equal(t, OutcomeUnreachable, outcome)
	mgr.err = nil
	now = now.Add(time.Minute)
	outcome, err = r.MaybeRestart(context.Background(), olderDispatcher(false))
	require.NoError(t, err)
	assert.Equal(t, OutcomeRestarted, outcome, "only a real restart starts the hourly limit")
	assert.Equal(t, 2, mgr.calls)
}
