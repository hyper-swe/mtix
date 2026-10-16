// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests fail-closed service boundaries and preservation of underlying failures.
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type wakeProtocolFault struct {
	*sqlite.Store
	floor int
	cause error
}

func (s wakeProtocolFault) MinDispatchProtocol(context.Context) (int, error) { return s.floor, s.cause }

type wakeAlertFault struct{ cause error }

func (a wakeAlertFault) Raise(context.Context, service.AlertRequest) (oplocal.Alert, error) {
	return oplocal.Alert{}, a.cause
}

func TestDispatcher_ProtocolReadFailure_RefusesWithDurableAlert(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	cause := errors.New("protocol backend unavailable")
	sent := 0
	d := wakeDispatcher(t, wakeProtocolFault{Store: s, cause: cause}, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
	require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}), cause)
	require.Zero(t, sent)
	rows, err := alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "wake.version-skew", rows[0].Class)
	require.NotContains(t, rows[0].Detail, cause.Error(), "protocol alert text does not interpolate backend output")
}

func TestDispatcher_ProtocolAlertFailure_PreservesBothCauses(t *testing.T) {
	s, _, _ := wakeServiceStore(t)
	require.NoError(t, s.SetMinDispatchProtocol(t.Context(), 2))
	cause := errors.New("operator alert refused")
	sent := 0
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: s, Alerts: wakeAlertFault{cause},
		Send: func(context.Context, model.WakeAttempt) error { sent++; return nil }})
	require.NoError(t, err)
	err = d.Pass(t.Context(), nil)
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, service.ErrWakeProtocolUnsupported)
	require.Zero(t, sent)
}

func TestNewWakeDispatcher_InvalidCapabilities_Refuses(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	for _, tc := range []struct {
		name   string
		change func(*service.WakeDispatcherOptions)
	}{
		{"store", func(o *service.WakeDispatcherOptions) { o.Store = nil }},
		{"alerts", func(o *service.WakeDispatcherOptions) { o.Alerts = nil }},
		{"sender", func(o *service.WakeDispatcherOptions) { o.Send = nil }},
		{"agent", func(o *service.WakeDispatcherOptions) { o.AlertAgent = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := service.WakeDispatcherOptions{Store: s, Alerts: alerts, Send: func(context.Context, model.WakeAttempt) error { return nil }}
			tc.change(&opts)
			_, err := service.NewWakeDispatcher(opts)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestDispatcher_InvalidCandidate_RejectsWholePassBeforeClaims(t *testing.T) {
	for _, change := range []func(*model.WakeKey){
		func(k *model.WakeKey) { k.AgentID = "" },
		func(k *model.WakeKey) { k.AgentID = " " },
		func(k *model.WakeKey) { k.HostID = "" },
		func(k *model.WakeKey) { k.UID = "" },
		func(k *model.WakeKey) { k.EventSeq = 0 },
		func(k *model.WakeKey) { k.Epoch = -1 },
	} {
		s, _, alerts := wakeServiceStore(t)
		sent := 0
		d := wakeDispatcher(t, s, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
		invalid := wakeServiceKey()
		change(&invalid)
		require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey(), invalid}), model.ErrInvalidInput)
		require.Zero(t, sent)
		stats, err := s.WakeLedgerStats(t.Context())
		require.NoError(t, err)
		require.Equal(t, model.WakeLedgerStats{}, stats)
	}
}

func TestDispatcher_CancellationBeforeAndBetweenCandidates_Stops(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	sent := 0
	d := wakeDispatcher(t, s, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
	require.ErrorIs(t, d.Pass(ctx, nil), context.Canceled)
	require.Zero(t, sent)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	d = wakeDispatcher(t, s, alerts, func(context.Context, model.WakeAttempt) error { sent++; cancel(); return nil })
	next := wakeServiceKey()
	next.EventSeq++
	require.ErrorIs(t, d.Pass(ctx, []model.WakeKey{wakeServiceKey(), next}), context.Canceled)
	require.Equal(t, 1, sent)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1}, stats)
}

type wakeClaimFault struct {
	*sqlite.Store
	cause error
}

func (s wakeClaimFault) ClaimWake(context.Context, model.WakeKey, time.Duration) (model.WakeAttempt, bool, error) {
	return model.WakeAttempt{}, false, s.cause
}

type wakeCompletionFault struct {
	*sqlite.Store
	cause error
}

func (s wakeCompletionFault) RecordWakeOutcome(context.Context, model.WakeAttempt, model.WakeOutcome) error {
	return s.cause
}

func TestDispatcher_StorageFailures_PreserveCause(t *testing.T) {
	for _, finish := range []bool{false, true} {
		s, _, alerts := wakeServiceStore(t)
		cause := errors.New("wake storage refused")
		var backend service.WakeStore = wakeClaimFault{Store: s, cause: cause}
		if finish {
			backend = wakeCompletionFault{Store: s, cause: cause}
		}
		sent := 0
		d := wakeDispatcher(t, backend, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
		require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}), cause)
		if finish {
			require.Equal(t, 1, sent)
		} else {
			require.Zero(t, sent)
		}
	}
}

func TestDispatcher_OneSendFails_OtherCandidateStillRuns(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	cause := errors.New("one send failed")
	sent := 0
	d := wakeDispatcher(t, s, alerts, func(_ context.Context, a model.WakeAttempt) error {
		sent++
		if a.Key.EventSeq == 7 {
			return cause
		}
		return nil
	})
	next := wakeServiceKey()
	next.EventSeq++
	require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey(), next}), cause)
	require.Equal(t, 2, sent)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 2}, stats)
}

func TestDispatcher_ReadableOpaqueAgentIDs_PreserveExistingPolicy(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	sent := 0
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: s, Alerts: alerts, AlertAgent: "agent/diagnostic",
		Send: func(context.Context, model.WakeAttempt) error { sent++; return nil }})
	require.NoError(t, err)
	key := wakeServiceKey()
	key.AgentID = "agent/worker"
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
	require.Equal(t, 1, sent)
	require.NoError(t, s.SetMinDispatchProtocol(t.Context(), 2))
	require.ErrorIs(t, d.Pass(t.Context(), nil), service.ErrWakeProtocolUnsupported)
	rows, err := alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "agent/diagnostic", rows[0].Agent)
}

type wakeMismatchedFence struct{ *sqlite.Store }

func (s wakeMismatchedFence) ClaimWake(_ context.Context, key model.WakeKey, _ time.Duration) (model.WakeAttempt, bool, error) {
	key.EventSeq++
	return model.WakeAttempt{Key: key, Token: "another-key-fence"}, true, nil
}

func TestDispatcher_MismatchedOrInvalidMetadata_Refuses(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	sent := 0
	d := wakeDispatcher(t, wakeMismatchedFence{s}, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
	require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}), model.ErrConflict)
	require.Zero(t, sent)
	d = wakeDispatcher(t, wakeProtocolFault{Store: s, floor: -1}, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
	require.ErrorIs(t, d.Pass(t.Context(), nil), model.ErrInvalidInput)
	require.Zero(t, sent)
}
