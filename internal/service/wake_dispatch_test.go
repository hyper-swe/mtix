// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests actual protocol refusal, durable operator alerts and terminal send outcomes.
package service_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func wakeServiceStore(t *testing.T) (*sqlite.Store, *time.Time, *service.Alerts) {
	t.Helper()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "wake.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	alerts := service.NewAlerts(operatorAlertsFixture(t), func() time.Time { return now })
	return s, &now, alerts
}

func wakeServiceKey() model.WakeKey {
	return model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: 7, Epoch: 1}
}

func wakeDispatcher(t *testing.T, backend service.WakeStore, alerts *service.Alerts, send func(context.Context, model.WakeAttempt) error) *service.WakeDispatcher {
	t.Helper()
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: backend, Alerts: alerts, Send: send})
	require.NoError(t, err)
	return d
}

func TestDispatcher_NewerMinProtocol_StopsWithAlert(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending event", true: "empty pass"}[empty], func(t *testing.T) {
			s, _, alerts := wakeServiceStore(t)
			require.NoError(t, s.SetMinDispatchProtocol(t.Context(), model.AttachProtocol+1))
			sent := 0
			d := wakeDispatcher(t, s, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
			keys := []model.WakeKey{wakeServiceKey()}
			if empty {
				keys = nil
			}
			require.ErrorIs(t, d.Pass(t.Context(), keys), service.ErrWakeProtocolUnsupported)
			require.Zero(t, sent)
			rows, err := alerts.Recent(t.Context(), 0)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "wake.version-skew", rows[0].Class)
			require.Contains(t, rows[0].Detail, "protocol 2")
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Equal(t, model.WakeLedgerStats{}, stats)
			floor, err := s.MinDispatchProtocol(t.Context())
			require.NoError(t, err)
			require.Equal(t, 2, floor)
		})
	}
}

func TestDispatcher_ProtocolRaisedBetweenPasses_RefusesNext(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	sent := 0
	d := wakeDispatcher(t, s, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.Equal(t, 1, sent)
	require.NoError(t, s.SetMinDispatchProtocol(t.Context(), 2))
	next := wakeServiceKey()
	next.EventSeq++
	require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{next}), service.ErrWakeProtocolUnsupported)
	require.Equal(t, 1, sent)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1}, stats)
}

func TestDispatcher_SendFailureAfterCancellation_RemainsTerminal(t *testing.T) {
	s, now, alerts := wakeServiceStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sendErr := errors.New("sender failed")
	sent := 0
	d := wakeDispatcher(t, s, alerts, func(context.Context, model.WakeAttempt) error {
		sent++
		cancel()
		return sendErr
	})
	require.ErrorIs(t, d.Pass(ctx, []model.WakeKey{wakeServiceKey()}), sendErr)
	var outcome string
	require.NoError(t, s.ReadDB().QueryRow("SELECT outcome FROM wake_ledger WHERE event_seq=?", 7).Scan(&outcome))
	require.Equal(t, string(model.WakeError), outcome)
	*now = now.Add(2 * time.Minute)
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.Equal(t, 1, sent)
}

type emptyWakeFence struct{ *sqlite.Store }

func (s emptyWakeFence) ClaimWake(_ context.Context, key model.WakeKey, _ time.Duration) (model.WakeAttempt, bool, error) {
	return model.WakeAttempt{Key: key}, true, nil
}

func TestDispatcher_EmptyAttemptFence_RefusesBeforeSend(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	sent := 0
	d := wakeDispatcher(t, emptyWakeFence{s}, alerts, func(context.Context, model.WakeAttempt) error { sent++; return nil })
	require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}), model.ErrConflict)
	require.Zero(t, sent)
}
