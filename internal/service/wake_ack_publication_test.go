// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type wakePublisherBackend struct {
	source             string
	mark               int64
	sourceErr, markErr error
}

func (b wakePublisherBackend) WakeBudgetSource(context.Context) (string, error) {
	return b.source, b.sourceErr
}
func (b wakePublisherBackend) WakeAckMark(context.Context, string) (int64, error) {
	return b.mark, b.markErr
}

func wakePublisherFixture(t *testing.T) (*oplocal.State, model.WakeRecipient, string) {
	t.Helper()
	state := testutil.OperatorState(t)
	host, err := state.HostID()
	require.NoError(t, err)
	key := model.WakeRecipient{AgentID: "worker", HostID: host, UID: "fixture-user"}
	source := "00000000-0000-4000-8000-000000000001"
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	require.NoError(t, state.UpdateWakeBudget(t.Context(), key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		m.Sources[source] = oplocal.WakeBudgetSource{Total: 5, Committed: 5, AckMark: 1}
		m.Credits = []oplocal.WakeBudgetCredit{{At: now, Count: 5}}
		m.Unacked, m.LastWakeAt, m.ObservedAt, m.NextWakeAt = 5, now, now, now.Add(time.Minute)
		return save()
	}))
	return state, key, source
}

func TestWakeAckPublication_FailuresNeverReturnRateCreditOrResetBackoff(t *testing.T) {
	cause := errors.New("ack publication dependency failed")
	for _, tc := range []struct {
		name    string
		backend wakePublisherBackend
		want    error
	}{
		{"source error", wakePublisherBackend{sourceErr: cause}, cause},
		{"coverage error", wakePublisherBackend{markErr: cause}, cause},
		{"rewound during publication", wakePublisherBackend{mark: 1}, model.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, key, source := wakePublisherFixture(t)
			backend := tc.backend
			backend.source = source
			publisher := NewWakeAckPublisher(backend, state, key.UID)
			require.ErrorIs(t, publisher.Observe(t.Context(), key.AgentID, 1, 2), tc.want)
			mirror, _, err := state.ReadWakeBudget(key)
			require.NoError(t, err)
			require.Equal(t, int64(5), mirror.Unacked)
			require.Equal(t, int64(5), mirror.Credits[0].Count)
		})
	}
}

func TestWakeAckPublication_NewAndIdempotentObservations(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		mark, before, after, unacked int64
	}{
		{"new ack", 2, 1, 2, 0}, {"fresh equal ordinal after restore", 1, 0, 1, 0},
		{"idempotent", 1, 1, 1, 5}, {"lower restored mark is fresh", 0, 0, 0, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, key, source := wakePublisherFixture(t)
			publisher := NewWakeAckPublisher(wakePublisherBackend{source: source, mark: tc.mark}, state, key.UID)
			require.NoError(t, publisher.Observe(t.Context(), key.AgentID, tc.before, tc.after))
			mirror, _, err := state.ReadWakeBudget(key)
			require.NoError(t, err)
			require.Equal(t, tc.unacked, mirror.Unacked)
			require.Equal(t, int64(5), mirror.Sources[source].Total)
			require.Equal(t, int64(5), mirror.Credits[0].Count)
		})
	}
}

func TestWakeAckPublication_MissingOrUnrelatedAccountingDoesNotInitialize(t *testing.T) {
	state := testutil.OperatorState(t)
	publisher := NewWakeAckPublisher(wakePublisherBackend{}, state, "fixture-user")
	require.NoError(t, publisher.Observe(t.Context(), "worker", 0, 1))
	host, err := state.ReadHostID()
	require.NoError(t, err)
	require.Empty(t, host)
	_, err = state.HostID()
	require.NoError(t, err)
	require.NoError(t, publisher.Observe(t.Context(), "worker", 0, 1))
	rows, err := state.WakeBudgetMirrors(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	require.ErrorIs(t, NewWakeAckPublisher(nil, nil, "").Observe(t.Context(), "worker", 0, 1), model.ErrInvalidInput)
}

func TestWakeAckPublication_IdentityAndContextErrorsPreserved(t *testing.T) {
	state, key, source := wakePublisherFixture(t)
	cause := errors.New("operator identity unavailable")
	p := &wakeAckPublisher{store: wakePublisherBackend{}, state: func() (*oplocal.State, string, error) { return nil, "", cause }}
	require.ErrorIs(t, p.Observe(t.Context(), "worker", 0, 1), cause)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	publisher := NewWakeAckPublisher(wakePublisherBackend{source: source, mark: 2}, state, key.UID)
	require.ErrorIs(t, publisher.Observe(ctx, key.AgentID, 1, 2), context.Canceled)
	require.NoError(t, p.Observe(t.Context(), "\n", 0, 1), "non-wakeable legacy ids have no guard effect")
}
