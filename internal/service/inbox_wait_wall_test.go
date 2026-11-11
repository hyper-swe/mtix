// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// waitBackend is a concurrency-safe inbox with counts, so a test can add an
// event while a wait is parked and count the full reads the wait performs.
type waitBackend struct {
	inboxAckBackend
	mu     sync.Mutex
	events []sqlite.InboxEvent
	lists  int
	counts int
}

func (b *waitBackend) InboxList(context.Context, string) ([]sqlite.InboxEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lists++
	return append([]sqlite.InboxEvent(nil), b.events...), nil
}

func (b *waitBackend) InboxWait(ctx context.Context, agent string, _ time.Duration) ([]sqlite.InboxEvent, error) {
	return b.InboxList(ctx, agent)
}

func (b *waitBackend) InboxCount(context.Context, string) (int64, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counts++
	n := int64(len(b.events))
	return n, n, nil
}

func (b *waitBackend) add(e sqlite.InboxEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
}

func (b *waitBackend) listCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lists
}

func eligibleNow() InboxPolicy {
	return InboxPolicy{Eligibility: func(context.Context, string) (InboxEligibility, error) {
		return InboxEligibility{Eligible: true}, nil
	}}
}

// MTIX-245 story 1: a wall clock that jumps past the timeout between ticks (a
// system sleep) ends the wait with the empty result within one tick, although
// the monotonic timer has barely advanced.
func TestInboxReadWait_WallClockJumpPastTimeout_ReturnsEmptyWithinOneTick(t *testing.T) {
	start := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	clock := func() time.Time {
		if calls.Add(1) <= 2 { // entry and the first probe
			return start
		}
		return start.Add(2 * time.Hour) // the machine slept
	}
	svc := NewInboxService(&waitBackend{}).WithInboxPolicy(eligibleNow()).WithWaitClock(clock)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	began := time.Now()
	res, err := svc.InboxReadWait(ctx, "w", time.Hour, InboxReadOptions{})
	require.NoError(t, err, "the wall-clock timeout is an empty result, not an error")
	assert.Empty(t, res.Events)
	assert.Less(t, time.Since(began), 3*time.Second, "returned within a tick, not at the monotonic timer")
}

// An unbounded wait (timeout 0) never expires on the wall clock.
func TestInboxReadWait_ZeroTimeoutWallJump_KeepsWaiting(t *testing.T) {
	start := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	clock := func() time.Time { return start.Add(time.Duration(calls.Add(1)) * 24 * time.Hour) }
	svc := NewInboxService(&waitBackend{}).WithInboxPolicy(eligibleNow()).WithWaitClock(clock)
	ctx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
	defer cancel()
	_, err := svc.InboxReadWait(ctx, "w", 0, InboxReadOptions{})
	require.Error(t, err, "only the caller's context ends an unbounded wait")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// MTIX-245 story 3: a parked wait probes counts each tick and does not run the
// full policy read again until the counts change or the floor passes.
func TestInboxReadWait_IdleTicks_SkipFullReadUntilProbeChanges(t *testing.T) {
	backend := &waitBackend{}
	svc := NewInboxService(backend).WithInboxPolicy(eligibleNow())
	res, err := svc.InboxReadWait(t.Context(), "w", 1200*time.Millisecond, InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 1, backend.listCalls(), "one full read at entry, none while the probe is unchanged and under the floor")
}

// A new event changes the probe, so the wait wakes on the next tick, well
// inside the full-read floor.
func TestInboxReadWait_EventArrivesWhileParked_WakesWithinATick(t *testing.T) {
	backend := &waitBackend{}
	svc := NewInboxService(backend).WithInboxPolicy(eligibleNow())
	go func() {
		time.Sleep(300 * time.Millisecond)
		backend.add(sqlite.InboxEvent{Seq: 1, Body: "go"})
	}()
	began := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 30*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Less(t, time.Since(began), inboxFullReadFloor, "woken by the probe, not by the floor")
}

// MTIX-245 story 3: eligibility can change with no inbox change (hub state,
// permissions, budget windows), so the floor still runs a full read and wakes.
func TestInboxReadWait_EligibilityFlipsWithoutInboxChange_WakesWithinFloor(t *testing.T) {
	backend := &waitBackend{events: []sqlite.InboxEvent{{Seq: 1, Body: "held"}}}
	var open atomic.Bool
	policy := InboxPolicy{Eligibility: func(context.Context, string) (InboxEligibility, error) {
		if open.Load() {
			return InboxEligibility{Eligible: true}, nil
		}
		return InboxEligibility{Reason: "unconfirmed"}, nil
	}}
	svc := NewInboxService(backend).WithInboxPolicy(policy)
	go func() {
		time.Sleep(400 * time.Millisecond)
		open.Store(true)
	}()
	began := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 30*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Less(t, time.Since(began), inboxFullReadFloor+inboxReadPollInterval+time.Second)
}
