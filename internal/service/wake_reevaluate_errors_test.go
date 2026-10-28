// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

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

func TestReevaluate_Dispatcher_RefusalsAndEmptyHolds(t *testing.T) {
	store, _, alerts := wakeServiceStore(t)
	state := testutil.OperatorState(t)
	build := func(backend service.WakeStore) *service.WakeDispatcher {
		d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: backend, Alerts: alerts, Settings: state, BudgetState: state, Clock: time.Now,
			Send: func(context.Context, model.WakeAttempt) error { return nil }})
		require.NoError(t, err)
		return d
	}
	d := build(store)
	require.NoError(t, d.Reevaluate(t.Context(), ""), "nothing held means nothing to do")
	require.NoError(t, d.Reevaluate(t.Context(), "worker"))
	require.ErrorIs(t, d.Reevaluate(t.Context(), "   "), model.ErrInvalidInput)
	plain := build(noHeldCandidates{store})
	require.ErrorIs(t, plain.Reevaluate(t.Context(), "worker"), model.ErrInvalidInput)
}

// noHeldCandidates hides the held-candidate capability while keeping delivery and accounting.
type noHeldCandidates struct{ s *sqlite.Store }

func (n noHeldCandidates) MinDispatchProtocol(ctx context.Context) (int, error) {
	return n.s.MinDispatchProtocol(ctx)
}

func (n noHeldCandidates) WakeEventHop(ctx context.Context, k model.WakeKey) (int64, error) {
	return n.s.WakeEventHop(ctx, k)
}

func (n noHeldCandidates) ClaimWake(ctx context.Context, k model.WakeKey, d time.Duration) (model.WakeAttempt, bool, error) {
	return n.s.ClaimWake(ctx, k, d)
}

func (n noHeldCandidates) RecordWakeOutcome(ctx context.Context, a model.WakeAttempt, o model.WakeOutcome) error {
	return n.s.RecordWakeOutcome(ctx, a, o)
}

func (n noHeldCandidates) ClaimWakeBudget(ctx context.Context, k model.WakeKey, ttl time.Duration,
	decide func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	return n.s.ClaimWakeBudget(ctx, k, ttl, decide)
}
