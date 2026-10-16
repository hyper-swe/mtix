// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type restartCountingManager struct{ calls int }

func (m *restartCountingManager) Restart(context.Context) error {
	m.calls++
	return nil
}

func restartDeps(st *oplocal.State, mgr attach.ServiceManager, marked bool, statuses ...service.DispatcherStatus) dispatcherRestartDeps {
	return dispatcherRestartDeps{
		statuses: statuses, manager: mgr, state: st, marked: marked,
		cliProtocol: 2, clock: func() time.Time { return attachTestNow },
	}
}

func TestRestartOlderDispatchers_Outcomes(t *testing.T) {
	older := service.DispatcherStatus{PID: 4242, Protocol: 1, Version: "0.6.0", Older: true}
	tests := []struct {
		name      string
		mgr       attach.ServiceManager
		marked    bool
		statuses  []service.DispatcherStatus
		wantText  []string
		wantAlert bool
		wantCalls int
	}{
		{"no heartbeat says nothing", &restartCountingManager{}, false, nil, nil, false, 0},
		{"current dispatcher says nothing", &restartCountingManager{}, false, []service.DispatcherStatus{{PID: 9, Protocol: 2}}, nil, false, 0},
		{"no dispatcher service is named honestly", attach.NoServiceManager{}, false, []service.DispatcherStatus{older}, []string{"PID 4242", "no dispatcher service is installed", "not restarted"}, true, 0},
		{"harness marker never restarts", &restartCountingManager{}, true, []service.DispatcherStatus{older}, []string{"PID 4242", "harness session", "not restarted"}, true, 0},
		{"reachable manager restarts once", &restartCountingManager{}, false, []service.DispatcherStatus{older}, []string{"restarted"}, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			var out bytes.Buffer
			restartOlderDispatchers(context.Background(), &out, restartDeps(st, tt.mgr, tt.marked, tt.statuses...))
			for _, text := range tt.wantText {
				assert.Contains(t, out.String(), text)
			}
			if tt.wantText == nil {
				assert.Empty(t, out.String())
			}
			alerts := attachAlerts(t, st)
			if tt.wantAlert {
				require.Len(t, alerts, 1)
				assert.Equal(t, "wake.version-skew", alerts[0].Class)
			} else {
				assert.Empty(t, alerts)
			}
			if m, ok := tt.mgr.(*restartCountingManager); ok {
				assert.Equal(t, tt.wantCalls, m.calls)
			}
		})
	}
}

func TestCLIRestartsDispatcher_UnderHarnessMarker_NeverThroughCLIPath(t *testing.T) {
	st := testutil.OperatorState(t)
	mgr := &restartCountingManager{}
	var out bytes.Buffer
	older := service.DispatcherStatus{PID: 4242, Protocol: 1, Older: true}
	restartOlderDispatchers(context.Background(), &out, restartDeps(st, mgr, true, older))
	restartOlderDispatchers(context.Background(), &out, restartDeps(st, mgr, true, older))
	assert.Zero(t, mgr.calls)
	assert.NotContains(t, out.String(), "restarted dispatcher")
}

func TestRestartOlderDispatchers_NoService_SecondAttemptNeverClaimsRestart(t *testing.T) {
	st := testutil.OperatorState(t)
	older := service.DispatcherStatus{PID: 4242, Protocol: 1, Version: "0.6.0", Older: true}
	var first, second bytes.Buffer
	restartOlderDispatchers(context.Background(), &first, restartDeps(st, attach.NoServiceManager{}, false, older))
	restartOlderDispatchers(context.Background(), &second, restartDeps(st, attach.NoServiceManager{}, false, older))
	for _, out := range []string{first.String(), second.String()} {
		assert.Contains(t, out, "no dispatcher service is installed")
		assert.NotContains(t, out, "was restarted")
		assert.NotContains(t, out, "restarted within")
	}
	alerts := attachAlerts(t, st)
	require.Len(t, alerts, 1)
	assert.Equal(t, uint64(2), alerts[0].Count, "every attempt raises the alert")
	assert.Contains(t, alerts[0].Fix, "4242")
	assert.NotEqual(t, "mtix doctor", alerts[0].Fix)
}

func TestRestartOlderDispatchers_RateLimited_RaisesAlert(t *testing.T) {
	st := testutil.OperatorState(t)
	mgr := &restartCountingManager{}
	older := service.DispatcherStatus{PID: 4242, Protocol: 1, Version: "0.6.0", Older: true}
	var first, second bytes.Buffer
	restartOlderDispatchers(context.Background(), &first, restartDeps(st, mgr, false, older))
	restartOlderDispatchers(context.Background(), &second, restartDeps(st, mgr, false, older))
	assert.Equal(t, 1, mgr.calls, "at most one restart per hour")
	assert.Contains(t, second.String(), "not restarted again")
	alerts := attachAlerts(t, st)
	require.Len(t, alerts, 1, "the rate-limited dispatcher is still reported")
	assert.Equal(t, "wake.version-skew", alerts[0].Class)
	assert.Contains(t, alerts[0].Fix, "4242")
}
