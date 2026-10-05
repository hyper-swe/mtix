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

type inboxAckBackend struct {
	ctx   context.Context
	agent string
	seq   int64
	err   error
	calls int
}

func (b *inboxAckBackend) InboxAck(ctx context.Context, agent string, seq int64) error {
	b.ctx = ctx
	b.agent = agent
	b.seq = seq
	b.calls++
	return b.err
}

func TestInboxService_AckPreservesInputsAndErrorChain(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"success", nil}, {"validation", model.ErrInvalidInput}, {"cancelled", context.Canceled}, {"backend failure", errors.New("durable journal unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := &inboxAckBackend{err: tt.err}
			svc := NewInboxService(backend)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := svc.InboxAck(ctx, "worker", 7)
			if tt.err == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.err)
				require.Equal(t, tt.err.Error(), err.Error())
				var operation interface{ Operation() string }
				require.ErrorAs(t, err, &operation)
				require.Equal(t, "ack inbox event 7 for worker", operation.Operation())
			}
			require.Equal(t, 1, backend.calls)
			require.Same(t, ctx, backend.ctx)
			require.Equal(t, "worker", backend.agent)
			require.Equal(t, int64(7), backend.seq)
		})
	}
}
