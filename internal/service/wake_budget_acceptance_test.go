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
)

func TestWakeBudget_SendSeesDurablyAcceptedMirror(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	b := f.board(t, 1)
	source, err := b.store.WakeBudgetSource(t.Context())
	require.NoError(t, err)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: b.store, Alerts: f.alerts, Settings: f.state, BudgetState: f.state,
		Clock: func() time.Time { return f.now },
		Send: func(_ context.Context, attempt model.WakeAttempt) error {
			mirror, present, err := f.state.ReadWakeBudget(attempt.Key.Recipient())
			require.NoError(t, err)
			require.True(t, present)
			row := mirror.Sources[source]
			require.Empty(t, row.Pending, "transport must see accepted accounting, not just pending intent")
			require.Equal(t, int64(1), row.Total)
			require.Equal(t, row.Total, row.Committed)
			f.sent.Add(1)
			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.Equal(t, int64(1), f.sent.Load())
	row := service.WakeBudgetDoctorCheck(b.store, f.state).Run(t.Context())
	require.Equal(t, 0, row.Data["pending_acceptances"])
}

func TestBackoff_SelectiveAckBelowCursorDoesNotReset(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 7)
	acks := service.NewInboxService(b.store)
	require.NoError(t, acks.InboxAckThrough(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[1:6]))
	require.NoError(t, acks.InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
	require.Equal(t, int64(5), f.sent.Load(), "selective insertion below the cumulative cursor acknowledges nothing new")
	require.NoError(t, acks.InboxAck(t.Context(), "budget-worker", b.keys[1].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
	require.Equal(t, int64(6), f.sent.Load(), "a genuinely new selective acknowledgement still resets")
}

// TestBackoff_ThroughAck_OnlyNewRecipientEventsReset reproduces the review probe:
// seq 1 is foreign (node creation), and seq 2 is already selectively acked.
func TestBackoff_ThroughAck_OnlyNewRecipientEventsReset(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 12)
	acks := service.NewInboxService(b.store)
	require.Equal(t, int64(2), b.keys[0].EventSeq)
	require.NoError(t, acks.InboxAck(t.Context(), "budget-worker", 2))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[1:6]))
	require.Equal(t, int64(5), f.sent.Load())
	require.NoError(t, acks.InboxAckThrough(t.Context(), "budget-worker", 1))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:7]))
	require.Equal(t, int64(5), f.sent.Load(), "foreign-only cursor movement cannot reset back-off")
	require.NoError(t, acks.InboxAckThrough(t.Context(), "budget-worker", 2))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:7]))
	require.Equal(t, int64(5), f.sent.Load(), "through 2 adds no recipient coverage")
	require.NoError(t, acks.InboxAckThrough(t.Context(), "budget-worker", 3))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:11]))
	require.Equal(t, int64(10), f.sent.Load(), "new recipient coverage resets back-off")
	require.NoError(t, b.store.Close())
	b.open(t)
	require.NoError(t, service.NewInboxService(b.store).InboxAckThrough(t.Context(), "budget-worker", 3))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[11:]))
	require.Equal(t, int64(10), f.sent.Load(), "repeated through-ack cannot reset again after reopening")
}
