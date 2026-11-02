// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// policyBackend is an inbox read backend with a returned-seq mark store.
type policyBackend struct {
	inboxReadBackend
	mark    int64
	charged map[int64]bool
}

func (b *policyBackend) WakeChargedAfter(context.Context, string, int64) (map[int64]bool, error) {
	return b.charged, nil
}

func (b *policyBackend) InboxReturnedMark(context.Context, string) (int64, error) { return b.mark, nil }
func (b *policyBackend) AdvanceInboxReturnedMark(_ context.Context, _ string, seq int64) error {
	b.mark = max(b.mark, seq)
	return nil
}

// countingBudget grants up to limit events and records each charge.
type countingBudget struct {
	limit, used int64
	charges     []int64
}

func (c *countingBudget) Locked(_ context.Context, _ string, fn func(AdmitFunc) error) error {
	return fn(func(want int64) (int64, error) {
		granted := min(want, c.limit-c.used)
		c.used += granted
		c.charges = append(c.charges, want)
		return granted, nil
	})
}

func eligible(budgeted bool) func(context.Context, string) (InboxEligibility, error) {
	return func(context.Context, string) (InboxEligibility, error) {
		return InboxEligibility{Eligible: true, Budgeted: budgeted}, nil
	}
}

func TestInboxRead_UnconfirmedID_HoldsAllAsCountOnly(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1, Body: "secret"}, {Seq: 2}}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) {
			return InboxEligibility{Reason: "unconfirmed"}, nil
		}})
	res, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Empty(t, res.HeldEvents, "held bodies are never returned by default")
	assert.Equal(t, InboxHeld{Count: 2, Reasons: []string{"unconfirmed"}}, res.Held)

	res, err = svc.InboxRead(t.Context(), "w", InboxReadOptions{IncludeHeld: true})
	require.NoError(t, err)
	assert.Len(t, res.HeldEvents, 2)
}

func TestInboxRead_PolicyError_FailsClosed(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) {
			return InboxEligibility{}, errors.New("state unreadable")
		}})
	res, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 1, res.Held.Count)
	assert.Contains(t, res.Held.Reasons[0], "state unreadable")
}

func TestInboxRead_NoPolicy_ReturnsEverythingUncounted(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}, {Seq: 2}}}}
	res, err := NewInboxService(backend).InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 2)
	assert.Zero(t, res.Held.Count)
	assert.NotNil(t, res.Held.Reasons)
}

// TestInboxRead_UnackedPlusNew_BudgetChargedOnce: an unacked event stays
// returned without a second charge when one new event arrives.
func TestInboxRead_UnackedPlusNew_BudgetChargedOnce(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}}}}
	budget := &countingBudget{limit: 12}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true), Budget: budget})

	res, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	backend.events = []sqlite.InboxEvent{{Seq: 1}, {Seq: 2}} // seq 1 never acked, seq 2 is new
	res, err = svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 2, "the unacked event is returned again")
	res, err = svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)

	assert.Equal(t, []int64{1, 1}, budget.charges, "one charge per newly returned event, none for repeats")
	assert.Equal(t, int64(2), budget.used)
	assert.Equal(t, int64(2), backend.mark)
}

// TestInboxRead_BudgetOfTwelve_ThirteenthHeld: with a configured budget of 12
// the 13th return does not happen early (ADR-007 B1).
func TestInboxRead_BudgetOfTwelve_ThirteenthHeld(t *testing.T) {
	var events []sqlite.InboxEvent
	for i := int64(1); i <= 13; i++ {
		events = append(events, sqlite.InboxEvent{Seq: i})
	}
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: events}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true), Budget: &countingBudget{limit: 12}})
	res, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{IncludeHeld: true})
	require.NoError(t, err)
	assert.Len(t, res.Events, 12)
	assert.Equal(t, InboxHeld{Count: 1, Reasons: []string{HeldReasonBudget}}, res.Held)
	require.Len(t, res.HeldEvents, 1)
	assert.Equal(t, int64(13), res.HeldEvents[0].Seq)
	assert.Equal(t, int64(12), backend.mark, "a held event is not marked returned")
}

func TestInboxRead_NotBudgeted_NeverCapped(t *testing.T) {
	var events []sqlite.InboxEvent
	for i := int64(1); i <= 40; i++ {
		events = append(events, sqlite.InboxEvent{Seq: i})
	}
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: events}}
	budget := &countingBudget{limit: 1}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(false), Budget: budget})
	res, err := svc.InboxRead(t.Context(), "loop", InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 40)
	assert.Empty(t, budget.charges)
	assert.Zero(t, backend.mark)
}

func TestInboxReadWait_AllHeld_TimesOutWithHeldCount(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) {
			return InboxEligibility{Reason: "unconfirmed"}, nil
		}})
	start := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 400*time.Millisecond, InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 1, res.Held.Count)
	assert.GreaterOrEqual(t, time.Since(start), 350*time.Millisecond, "a held-only inbox must not end the wait")
}

func TestInboxReadWait_LevelTriggered_ReturnsUnackedEventImmediately(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 5}}}, mark: 5}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true), Budget: &countingBudget{limit: 1}})
	res, err := svc.InboxReadWait(t.Context(), "w", time.Second, InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 1)
}

func TestInboxReadWait_InvalidAndCancelled_Errors(t *testing.T) {
	svc := NewInboxService(&policyBackend{}).WithInboxPolicy(InboxPolicy{Eligibility: eligible(false)})
	_, err := svc.InboxReadWait(t.Context(), "w", -time.Second, InboxReadOptions{})
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = svc.InboxReadWait(ctx, "w", time.Minute, InboxReadOptions{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = NewInboxServiceWithReads(&inboxAckBackend{}, nil).InboxRead(t.Context(), "w", InboxReadOptions{})
	require.Error(t, err)
}

func TestPolicyInboxReader_DropsHeldEvents(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) { return InboxEligibility{Reason: "x"}, nil }})
	got, err := NewPolicyInboxReader(svc).InboxWait(t.Context(), "w", 300*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Events the wake dispatcher already charged are returned without a read charge,
// and the mark stops before the first held event.
func TestInboxRead_DispatcherChargedEvents_FreeAndMarkStopsAtHeld(t *testing.T) {
	events := []sqlite.InboxEvent{{Seq: 1}, {Seq: 2}, {Seq: 3}, {Seq: 4}}
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: events}, charged: map[int64]bool{2: true, 4: true}}
	budget := &countingBudget{limit: 1}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true), Budget: budget})
	res, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{IncludeHeld: true})
	require.NoError(t, err)
	var got []int64
	for _, e := range res.Events {
		got = append(got, e.Seq)
	}
	assert.Equal(t, []int64{1, 2, 4}, got, "charged 2 and 4 are free; the one budget unit goes to 1")
	require.Len(t, res.HeldEvents, 1)
	assert.Equal(t, int64(3), res.HeldEvents[0].Seq)
	assert.Equal(t, []int64{2}, budget.charges, "only events 1 and 3 needed a charge")
	assert.Equal(t, int64(2), backend.mark, "the mark stops before the first held event")
}

type countBackend struct {
	inboxAckBackend
	pending, total int64
}

func (c *countBackend) InboxCount(context.Context, string) (int64, int64, error) {
	return c.pending, c.total, nil
}

func TestInboxCountPolicy_HeldIDReportsZeroPending(t *testing.T) {
	backend := &countBackend{pending: 3, total: 5}
	held := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) { return InboxEligibility{Reason: "x"}, nil }})
	pending, total, heldCount, err := held.InboxCountPolicy(t.Context(), "w")
	require.NoError(t, err)
	assert.Equal(t, []int64{0, 5, 3}, []int64{pending, total, heldCount})
	open := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(false)})
	pending, _, heldCount, err = open.InboxCountPolicy(t.Context(), "w")
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 0}, []int64{pending, heldCount})
}
