// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wakeLeaseTestHolder = "0123456789abcdef0123456789abcdef"

type wakeLeaseProbeCall struct {
	ctx              context.Context
	key              service.WakeLeaseKey
	holder           string
	ttl              time.Duration
	acquire, abandon bool
}

type wakeLeaseBackendProbe struct {
	calls  []wakeLeaseProbeCall
	lease  service.WakeLease
	result service.WakeLeaseReleaseResult
	err    error
}

func (p *wakeLeaseBackendProbe) AcquireWakeLease(ctx context.Context, key service.WakeLeaseKey, holder string, ttl time.Duration) (service.WakeLease, error) {
	p.calls = append(p.calls, wakeLeaseProbeCall{ctx: ctx, key: key, holder: holder, ttl: ttl, acquire: true})
	return p.lease, p.err
}

func (p *wakeLeaseBackendProbe) ReleaseWakeLease(ctx context.Context, key service.WakeLeaseKey, holder string, ttl time.Duration, abandon bool) (service.WakeLeaseReleaseResult, error) {
	p.calls = append(p.calls, wakeLeaseProbeCall{ctx: ctx, key: key, holder: holder, ttl: ttl, abandon: abandon})
	return p.result, p.err
}

type wakeLeaseOperation struct {
	name             string
	acquire, abandon bool
	run              func(context.Context, service.WakeLeaseKey, time.Duration) (any, error)
}

func wakeLeaseOperations(svc *service.WakeLeaseService, holder string) []wakeLeaseOperation {
	return []wakeLeaseOperation{
		{"acquire", true, false, func(ctx context.Context, key service.WakeLeaseKey, ttl time.Duration) (any, error) {
			return svc.Acquire(ctx, key, ttl)
		}},
		{"renew", true, false, func(ctx context.Context, key service.WakeLeaseKey, ttl time.Duration) (any, error) {
			return svc.Renew(ctx, key, holder, ttl)
		}},
		{"release", false, false, func(ctx context.Context, key service.WakeLeaseKey, ttl time.Duration) (any, error) {
			return svc.Release(ctx, key, holder, ttl)
		}},
		{"abandon", false, true, func(ctx context.Context, key service.WakeLeaseKey, ttl time.Duration) (any, error) {
			return svc.Abandon(ctx, key, holder, ttl)
		}},
	}
}

func wakeLeaseServiceKey() service.WakeLeaseKey {
	return service.WakeLeaseKey{AgentID: "TEST-agent", HostID: "TEST-host", UID: "1000"}
}

func wakeLeaseServiceTime() time.Time {
	return time.Date(2026, 10, 16, 0, 0, 0, 123, time.UTC)
}

func TestWakeLeaseService_ValidRequests_PreservesIntentAndResults(t *testing.T) {
	for _, ttl := range []time.Duration{time.Minute, 24 * time.Hour} {
		for index := range 4 {
			probe := &wakeLeaseBackendProbe{}
			operations := wakeLeaseOperations(service.NewWakeLeaseService(probe), wakeLeaseTestHolder)
			op := operations[index]
			t.Run(op.name+"/"+ttl.String(), func(t *testing.T) {
				now := wakeLeaseServiceTime()
				probe.lease = service.WakeLease{AgentID: "TEST-agent", Holder: wakeLeaseTestHolder, HighWater: 23, AcquiredAt: now, ExpiresAt: now.Add(ttl)}
				expires := now.Add(ttl)
				probe.result = service.WakeLeaseReleaseResult{Outcome: "relaunch", Pending: 3, HighWater: 29, ExpiresAt: &expires}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				got, err := op.run(ctx, wakeLeaseServiceKey(), ttl)
				require.NoError(t, err)
				require.Len(t, probe.calls, 1)
				holder := wakeLeaseTestHolder
				if op.name == "acquire" {
					holder = ""
				}
				assert.Equal(t, wakeLeaseProbeCall{ctx: ctx, key: wakeLeaseServiceKey(), holder: holder, ttl: ttl, acquire: op.acquire, abandon: op.abandon}, probe.calls[0])
				if op.acquire {
					assert.Equal(t, probe.lease, got)
				} else {
					assert.Equal(t, probe.result, got)
				}
			})
		}
	}
}

func TestWakeLeaseService_BackendFailure_PreservesValueAndDiagnostic(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{"canceled", context.Canceled}, {"held", model.ErrAlreadyClaimed}, {"disk full", model.ErrDiskFull},
		{"corrupt", model.ErrCorrupted}, {"storage", errors.New("synthetic storage failure")},
		{"held and disk full", errors.Join(model.ErrAlreadyClaimed, model.ErrDiskFull)},
	}
	for _, failure := range failures {
		for index := range 4 {
			probe := &wakeLeaseBackendProbe{err: failure.err, lease: service.WakeLease{ExpiresAt: wakeLeaseServiceTime()}}
			op := wakeLeaseOperations(service.NewWakeLeaseService(probe), wakeLeaseTestHolder)[index]
			t.Run(op.name+"/"+failure.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				got, err := op.run(ctx, wakeLeaseServiceKey(), time.Minute)
				require.ErrorIs(t, err, failure.err)
				assert.Equal(t, failure.err.Error(), err.Error())
				require.Len(t, probe.calls, 1)
				assert.Same(t, ctx, probe.calls[0].ctx)
				if op.acquire {
					assert.Equal(t, probe.lease, got)
				} else {
					assert.Equal(t, probe.result, got)
				}
			})
		}
	}
}

func TestWakeLeaseService_LegacyNonemptyIdentity_PassesUnmodified(t *testing.T) {
	probe := &wakeLeaseBackendProbe{}
	key := service.WakeLeaseKey{AgentID: "legacy agent", HostID: "legacy host", UID: "legacy user"}
	_, err := service.NewWakeLeaseService(probe).Acquire(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.Len(t, probe.calls, 1)
	assert.Equal(t, key, probe.calls[0].key)
}
