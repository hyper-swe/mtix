// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

type atomicAckPublicationCount struct{ calls atomic.Int64 }

func (p *atomicAckPublicationCount) Observe(context.Context, string, int64, int64) error {
	p.calls.Add(1)
	return nil
}

func TestInboxWakeAck_ConcurrentSameEvent_PublishesOneEffectiveAck(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	b := f.board(t, 1)
	publisher := &atomicAckPublicationCount{}
	svc := service.NewInboxServiceWithWakeAcks(b.store, nil, publisher)
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() { results <- svc.InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq) })
	}
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), publisher.calls.Load(), "a concurrent idempotent writer must not publish a second backoff reset")
}
