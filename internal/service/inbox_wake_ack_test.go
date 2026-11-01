// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type wakeCoverageAckBackend struct {
	before, after                 int64
	beforeErr, afterErr, writeErr error
	observations, writes          int
}

func (b *wakeCoverageAckBackend) WakeAckMark(context.Context, string) (int64, error) {
	b.observations++
	if b.observations == 1 {
		return b.before, b.beforeErr
	}
	return b.after, b.afterErr
}

func (b *wakeCoverageAckBackend) InboxAck(context.Context, string, int64) error {
	b.writes++
	return b.writeErr
}

func (b *wakeCoverageAckBackend) InboxAckThrough(ctx context.Context, agent string, seq int64) error {
	return b.InboxAck(ctx, agent, seq)
}

type wakeAckPublisherSpy struct {
	cause error
	calls int
}

func (p *wakeAckPublisherSpy) Observe(context.Context, string, int64, int64) error {
	p.calls++
	return p.cause
}

type wakeAckBoundaryCase struct {
	name                                      string
	beforeErr, afterErr, writeErr, publishErr error
	after                                     int64
	writes, publishes                         int
	want                                      error
}

func TestInboxWakeAck_ErrorBoundariesPreserveCausesAndAcceptance(t *testing.T) {
	cause := errors.New("acknowledgement dependency failed")
	cases := []wakeAckBoundaryCase{
		{name: "before read", beforeErr: cause, want: cause},
		{name: "write refused", writeErr: cause, writes: 1, want: cause},
		{name: "after read", afterErr: cause, writes: 1, want: cause},
		{name: "coverage rewind", after: 0, writes: 1, want: model.ErrConflict},
		{name: "idempotent", after: 1, writes: 1},
		{name: "new ack", after: 2, writes: 1, publishes: 1},
		{name: "publication failed after accepted write", after: 2, publishErr: cause, writes: 1, publishes: 1, want: cause},
	}
	for _, cumulative := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(map[bool]string{false: "selective/", true: "cumulative/"}[cumulative]+tc.name, func(t *testing.T) {
				backend := &wakeCoverageAckBackend{before: 1, after: tc.after, beforeErr: tc.beforeErr, afterErr: tc.afterErr, writeErr: tc.writeErr}
				publisher := &wakeAckPublisherSpy{cause: tc.publishErr}
				svc := NewInboxServiceWithWakeAcks(backend, nil, publisher)
				ack := svc.InboxAck
				if cumulative {
					ack = svc.InboxAckThrough
				}
				err := ack(t.Context(), "worker", 7)
				require.ErrorIs(t, err, tc.want)
				require.Equal(t, tc.writes, backend.writes)
				require.Equal(t, tc.publishes, publisher.calls)
			})
		}
	}
}

func TestInboxWakeAck_OptionalCapabilitiesRemainNarrow(t *testing.T) {
	plain := &inboxAckBackend{}
	require.ErrorIs(t, NewInboxService(plain).InboxAckThrough(t.Context(), "worker", 1), model.ErrInvalidInput)
	backend := &wakeCoverageAckBackend{}
	svc := NewInboxServiceWithWakeAcks(backend, nil, nil)
	require.NoError(t, svc.InboxAck(t.Context(), "worker", 1))
	require.Zero(t, backend.observations)
	require.Equal(t, 1, backend.writes)
}
