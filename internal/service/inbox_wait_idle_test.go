// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// watchBackend is a waitBackend with a change token a test moves by hand
// (MTIX-326).
type watchBackend struct {
	waitBackend
	ver     atomic.Int64
	reads   atomic.Int64
	failing atomic.Bool
}

func (b *watchBackend) InboxChangeToken(context.Context) (string, error) {
	b.reads.Add(1)
	if b.failing.Load() {
		return "", errors.New("stat failed")
	}
	return strconv.FormatInt(b.ver.Load(), 10), nil
}

func (b *watchBackend) countCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts
}

func newWatchBackend() *watchBackend { return &watchBackend{} }

// MTIX-326 acceptance 1: with the database unchanged an idle wait asks no
// count and does no read after its entry read.
func TestInboxReadWait_IdleUnchangedDB_NoCountOrReadAfterEntry(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow())
	res, err := svc.InboxReadWait(t.Context(), "w", 1600*time.Millisecond, InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 1, b.listCalls(), "the entry read only")
	assert.Equal(t, 1, b.countCalls(), "the entry probe only")
}

// Acceptance 1: the safety read still runs, once per floor, when nothing moved.
func TestInboxReadWait_IdleUnchangedDB_SafetyReadRunsPerFloor(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow()).WithWaitSafetyFloor(700 * time.Millisecond)
	_, err := svc.InboxReadWait(t.Context(), "w", 2*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, b.listCalls(), 2, "a safety read ran")
	assert.LessOrEqual(t, b.listCalls(), 4, "but only about one per floor")
}

// Acceptance 2: a new addressed event wakes the waiter within 2 s.
func TestInboxReadWait_EventAfterVersionMoves_WakesWithin2s(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow())
	go func() {
		time.Sleep(400 * time.Millisecond)
		b.add(sqlite.InboxEvent{Seq: 1, Body: "go"})
		b.ver.Add(1)
	}()
	began := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 30*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Less(t, time.Since(began), 2*time.Second)
}

// An unrelated commit moves the token: one count, no full read, and the
// token is remembered so the next tick does not count again.
func TestInboxReadWait_UnrelatedWrite_CostsOneCountNoRead(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow())
	go func() {
		time.Sleep(300 * time.Millisecond)
		b.ver.Add(1)
	}()
	_, err := svc.InboxReadWait(t.Context(), "w", 1600*time.Millisecond, InboxReadOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, b.listCalls())
	assert.Equal(t, 2, b.countCalls(), "entry probe plus one for the moved token")
}

// A failing token read must not strand the waiter: it falls back to counting.
func TestInboxReadWait_TokenFails_FallsBackToCountProbe(t *testing.T) {
	b := newWatchBackend()
	b.failing.Store(true)
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow())
	go func() {
		time.Sleep(300 * time.Millisecond)
		b.add(sqlite.InboxEvent{Seq: 1, Body: "go"})
	}()
	res, err := svc.InboxReadWait(t.Context(), "w", 5*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
}

// A commit that moves no inbox count (hub state, permissions) still reaches a
// full read within the old 2 s cadence, so an eligibility flip is not delayed
// to the safety floor.
func TestInboxReadWait_CommitFlipsEligibility_WakesWithinDirtyDelay(t *testing.T) {
	b := newWatchBackend()
	b.add(sqlite.InboxEvent{Seq: 1, Body: "held"})
	var open atomic.Bool
	svc := NewInboxService(b).WithInboxPolicy(InboxPolicy{Eligibility: func(context.Context, string) (InboxEligibility, error) {
		if open.Load() {
			return InboxEligibility{Eligible: true}, nil
		}
		return InboxEligibility{Reason: "unconfirmed"}, nil
	}})
	go func() {
		time.Sleep(300 * time.Millisecond)
		open.Store(true)
		b.ver.Add(1)
	}()
	began := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 30*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Less(t, time.Since(began), inboxDirtyReadDelay+inboxReadPollInterval+time.Second)
}
