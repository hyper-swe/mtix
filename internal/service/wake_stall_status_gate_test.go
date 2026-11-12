// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Stalled is derived only for a registration on the hook route and only while wake.live is
// on: an agent whose route never arms a waiter, or whose live wake is off, is never told its
// turn has not ended (MTIX-112.40.12).
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func gatedStatus(now *time.Time, liveOn bool, hook func(context.Context, string) (bool, error)) *service.WakeStatusService {
	return service.NewWakeStatusService(service.WakeStatusDeps{
		Settings:   func(string) (bool, time.Duration, error) { return liveOn, stallWindow, nil },
		Registered: func(context.Context, string) (bool, error) { return true, nil },
		Dispatcher: func(context.Context) (bool, error) { return true, nil },
		Rows: func(context.Context, string, int) ([]model.WakeStatusRow, error) {
			return nil, nil
		},
		Pending: func(context.Context, string) ([]service.PendingEvent, error) {
			return []service.PendingEvent{{Seq: 5, At: now.Add(-2 * stallWindow)}}, nil
		},
		WaiterLive: func(context.Context, string) (bool, error) { return false, nil },
		HookRoute:  hook,
		Clock:      func() time.Time { return *now },
	})
}

func TestStatusStalled_RouteAndLiveSwitch_GateTheDerivation(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	hookOn := func(context.Context, string) (bool, error) { return true, nil }
	hookOff := func(context.Context, string) (bool, error) { return false, nil }
	tests := []struct {
		name    string
		liveOn  bool
		hook    func(context.Context, string) (bool, error)
		stalled bool
	}{
		{"hook route and live wake on", true, hookOn, true},
		{"route reader absent is the hook route", true, nil, true},
		{"registration not on the hook route", true, hookOff, false},
		{"live wake off", false, hookOn, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := gatedStatus(&now, tt.liveOn, tt.hook)
			require.Equal(t, tt.stalled, svc.Current(t.Context(), "test-agent").State == model.WakeStateStalled)
			recent, err := svc.Recent(t.Context(), "test-agent", 10)
			require.NoError(t, err)
			got := false
			for _, r := range recent {
				got = got || r.State == model.WakeStateStalled
			}
			require.Equal(t, tt.stalled, got)
		})
	}
}
