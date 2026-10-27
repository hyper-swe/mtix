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
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type synchronizedAckReads struct {
	*sqlite.Store
	reads atomic.Int64
	ready sync.WaitGroup
}

func (s *synchronizedAckReads) WakeAckMark(ctx context.Context, agent string) (int64, error) {
	mark, err := s.Store.WakeAckMark(ctx, agent)
	if s.reads.Add(1) <= 2 {
		s.ready.Done()
		s.ready.Wait()
	}
	return mark, err
}

type atomicAckPublicationCount struct{ calls atomic.Int64 }

func (p *atomicAckPublicationCount) Observe(context.Context, string, int64, int64) error {
	p.calls.Add(1)
	return nil
}

func TestInboxWakeAck_ConcurrentSameEvent_PublishesOneEffectiveAck(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	b := f.board(t, 1)
	backend := &synchronizedAckReads{Store: b.store}
	backend.ready.Add(2)
	publisher := &atomicAckPublicationCount{}
	svc := service.NewInboxServiceWithWakeAcks(backend, nil, publisher)
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
