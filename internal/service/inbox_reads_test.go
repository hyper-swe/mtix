// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type inboxCallerContextKey struct{}

type inboxReadBackend struct {
	inboxAckBackend
	events  []sqlite.InboxEvent
	timeout time.Duration
}

func (b *inboxReadBackend) InboxList(ctx context.Context, agent string) ([]sqlite.InboxEvent, error) {
	b.ctx = ctx
	b.agent = agent
	b.calls++
	return b.events, b.err
}
func (b *inboxReadBackend) InboxWait(ctx context.Context, agent string, timeout time.Duration) ([]sqlite.InboxEvent, error) {
	b.timeout = timeout
	return b.InboxList(ctx, agent)
}

func TestInboxService_ReadsPreserveInputsEventsAndErrorChain(t *testing.T) {
	for _, failure := range []error{nil, context.Canceled, errors.New("journal unavailable")} {
		for _, wait := range []bool{false, true} {
			backend := &inboxReadBackend{inboxAckBackend: inboxAckBackend{err: failure}, events: []sqlite.InboxEvent{{Seq: 7}, {Seq: 9}}}
			svc := NewInboxService(backend)
			ctx := context.WithValue(t.Context(), inboxCallerContextKey{}, "caller")
			events, err := svc.InboxList(ctx, "worker")
			if wait {
				backend.calls = 0
				events, err = svc.InboxWait(ctx, "worker", 3*time.Second)
				require.Equal(t, 3*time.Second, backend.timeout)
			}
			require.Equal(t, 1, backend.calls)
			require.Same(t, ctx, backend.ctx)
			require.Equal(t, "worker", backend.agent)
			if failure != nil {
				require.ErrorIs(t, err, failure)
				require.Equal(t, failure.Error(), err.Error())
				continue
			}
			require.NoError(t, err)
			require.Equal(t, []InboxEvent{{Seq: 7}, {Seq: 9}}, events)
		}
	}
}

func TestInboxService_ReadsSupportNilEmptyAndAckOnlyBackends(t *testing.T) {
	for _, events := range [][]sqlite.InboxEvent{nil, {}} {
		svc := NewInboxService(&inboxReadBackend{events: events})
		got, err := svc.InboxList(t.Context(), "worker")
		require.NoError(t, err)
		require.Equal(t, events == nil, got == nil)
		require.Empty(t, got)
	}
	svc := NewInboxService(&inboxAckBackend{})
	_, err := svc.InboxList(t.Context(), "worker")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = svc.InboxWait(t.Context(), "worker", time.Second)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.NoError(t, svc.InboxAck(t.Context(), "worker", 7))
}
