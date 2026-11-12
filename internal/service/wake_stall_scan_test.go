// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that stalled-turn recovery (MTIX-112.40.12, FR-20) reads no ledger candidates when
// no push route can redeliver, so an idle host pays nothing per dispatcher pass.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// countingLister counts candidate scans.
type countingLister struct {
	*sqlite.Store
	scans int
}

func (c *countingLister) StalledWakeCandidates(ctx context.Context, q sqlite.StalledWakeQuery) ([]model.WakeKey, error) {
	c.scans++
	return c.Store.StalledWakeCandidates(ctx, q)
}

func TestStalledRecovery_NoPushRouteLive_ReadsNoCandidates(t *testing.T) {
	st, _, alerts := wakeServiceStore(t)
	counter := &countingLister{Store: st}
	state := testutil.OperatorState(t)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: counter, Alerts: alerts, Adapters: []service.WakeAdapter{armed(service.RouteHookWaiter), armed(service.RouteInboxOnly)}, Settings: state, BudgetState: state, Clock: time.Now, Held: func(string) {},
		HostID: bothRoutesRecipient.HostID, UID: bothRoutesRecipient.UID,
		Stall: &service.WakeStallDeps{
			Window:     func(string) (time.Duration, error) { return stallWindow, nil },
			WaiterLive: func(context.Context, model.WakeKey) (bool, error) { return false, nil },
		},
	})
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Zero(t, counter.scans, "no adapter can redeliver: the pass must not scan the ledger")
}

func TestStalledRecovery_PushRouteLive_ReadsCandidates(t *testing.T) {
	st, _, alerts := wakeServiceStore(t)
	counter := &countingLister{Store: st}
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcherOn(t, st, counter, alerts, tally, nil)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Positive(t, counter.scans, "control: a live push route does scan")
}
