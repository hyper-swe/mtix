// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func budgetedPolicy(f *wakeBudgetFixture) service.InboxPolicy {
	return service.InboxPolicy{
		Eligibility: func(context.Context, string) (service.InboxEligibility, error) {
			return service.InboxEligibility{Eligible: true, Budgeted: true}, nil
		},
		Budget: service.WakeInboxBudget{State: f.state, HostID: f.host, UID: f.uid, Clock: func() time.Time { return f.now }},
	}
}

func credits(t *testing.T, f *wakeBudgetFixture, agent string) int64 {
	t.Helper()
	m, present, err := f.state.ReadWakeBudget(model.WakeRecipient{AgentID: agent, HostID: f.host, UID: f.uid})
	require.NoError(t, err)
	if !present {
		return 0
	}
	var total int64
	for _, c := range m.Credits {
		total += c.Count
	}
	return total
}

// S1 (MTIX-112.18.1 r1): the dispatcher charged this event when it woke the
// agent; at a limit of 1 the read must return it, not hold it as spent.
func TestInboxRead_WokenEventAtLimitOne_ReturnedNotHeld(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	b := f.board(t, 1)
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys))
	require.Equal(t, int64(1), f.sent.Load())
	require.Equal(t, int64(1), credits(t, f, "budget-worker"))

	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 1, "a woken agent can read the event that woke it")
	assert.Zero(t, res.Held.Count)
	assert.Equal(t, int64(1), credits(t, f, "budget-worker"), "one event costs one credit across both paths")
}

// The mirror image: a read charged first, the wake that follows is not charged
// again and is not held by the spent budget.
func TestWake_AfterInboxRead_NotChargedAgain(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	b := f.board(t, 1)
	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	require.Equal(t, int64(1), credits(t, f, "budget-worker"))

	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys))
	assert.Equal(t, int64(1), f.sent.Load(), "the budget is not spent by the event the agent already read")
	assert.Equal(t, int64(1), credits(t, f, "budget-worker"))
}

// Concurrent reads of one id charge each event once (run with -race).
func TestInboxRead_ConcurrentReads_ChargeEachEventOnce(t *testing.T) {
	f := newWakeBudgetFixture(t, "5")
	b := f.board(t, 5)
	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	counts := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
			errs <- err
			counts <- len(res.Events)
		}()
	}
	wg.Wait()
	close(errs)
	close(counts)
	for err := range errs {
		require.NoError(t, err)
	}
	for n := range counts {
		assert.Equal(t, 5, n, "every reader sees all five events")
	}
	assert.Equal(t, int64(5), credits(t, f, "budget-worker"), "five events, five credits, however many readers")
}
