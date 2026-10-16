// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Optional count capabilities preserve ack-only and separately injected reads.
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type countCapability interface {
	InboxCount(context.Context, string) (int64, int64, error)
}
type inboxCountBackend struct {
	inboxReadBackend
	pending, total int64
}

func (b *inboxCountBackend) InboxCount(ctx context.Context, agent string) (int64, int64, error) {
	b.ctx = ctx
	b.agent = agent
	b.calls++
	return b.pending, b.total, b.err
}

func requireCountService(t *testing.T, s *InboxService) countCapability {
	t.Helper()
	counter, ok := any(s).(countCapability)
	require.True(t, ok, "service must own inbox count")
	return counter
}

func TestInboxService_CountPreservesInputsAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		failure error
	}{{"success", nil}, {"cancelled", context.Canceled}, {"backend", errors.New("journal unavailable")}} {
		t.Run(tt.name, func(t *testing.T) {
			b := &inboxCountBackend{pending: 2, total: 9}
			b.err = tt.failure
			s := requireCountService(t, NewInboxService(b))
			ctx := context.WithValue(t.Context(), inboxCallerContextKey{}, "count")
			p, n, err := s.InboxCount(ctx, "w")
			if tt.failure == nil {
				require.NoError(t, err)
				require.Equal(t, int64(2), p)
				require.Equal(t, int64(9), n)
			} else {
				require.ErrorIs(t, err, tt.failure)
			}
			require.Equal(t, 1, b.calls)
			require.Same(t, ctx, b.ctx)
			require.Equal(t, "w", b.agent)
		})
	}
}

func TestInboxService_CountOptionalBackend_PreservesCompatibility(t *testing.T) {
	ack := &inboxAckBackend{}
	reader := &inboxCountBackend{pending: 3, total: 5}
	s := requireCountService(t, NewInboxServiceWithReads(ack, reader))
	p, n, err := s.InboxCount(t.Context(), "w")
	require.NoError(t, err)
	require.Equal(t, int64(3), p)
	require.Equal(t, int64(5), n)
	require.Zero(t, ack.calls)
	missing := NewInboxService(ack)
	c := requireCountService(t, missing)
	_, _, err = c.InboxCount(t.Context(), "w")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.NoError(t, missing.InboxAck(t.Context(), "w", 1))
	for _, agent := range []string{"", " ", "bad\nagent"} {
		_, _, err = s.InboxCount(t.Context(), agent)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	}
	require.Equal(t, 1, reader.calls)
}

func TestInboxService_CountFallbackAndValidation_UsesOnlyCapableBackend(t *testing.T) {
	backend := &inboxCountBackend{pending: 1, total: 2}
	reader := &inboxReadBackend{}
	counter := requireCountService(t, NewInboxServiceWithReads(backend, reader))
	p, n, err := counter.InboxCount(t.Context(), "Upper Case's agent")
	require.NoError(t, err)
	require.Equal(t, int64(1), p)
	require.Equal(t, int64(2), n)
	require.Equal(t, "Upper Case's agent", backend.agent)
	require.Zero(t, reader.calls)
	for _, agent := range []string{strings.Repeat("x", 65), "w\u200b", "\u202e"} {
		_, _, err = counter.InboxCount(t.Context(), agent)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	}
	require.Equal(t, 1, backend.calls)
}
