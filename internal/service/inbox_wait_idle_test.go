// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// idleWaitLong is longer than inboxFullReadFloor, so a wait that wrongly runs
// a full read at the old 2 s cadence shows in the call counts (MTIX-326 S3).
const idleWaitLong = inboxFullReadFloor + 600*time.Millisecond

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
// count and does no read after its entry read, for longer than the old 2 s
// full-read cadence.
func TestInboxReadWait_IdleUnchangedDB_NoCountOrReadAfterEntry(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow())
	res, err := svc.InboxReadWait(t.Context(), "w", idleWaitLong, InboxReadOptions{})
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

// An unrelated commit moves the token: one count and no full read at all, for
// longer than the old 2 s cadence; the token is remembered so the next tick
// does not count again.
func TestInboxReadWait_UnrelatedWrite_CostsOneCountNoRead(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow())
	go func() {
		time.Sleep(300 * time.Millisecond)
		b.ver.Add(1)
	}()
	_, err := svc.InboxReadWait(t.Context(), "w", idleWaitLong, InboxReadOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, b.listCalls(), "an unrelated commit costs no full read")
	assert.Equal(t, 2, b.countCalls(), "entry probe plus one for the moved token")
}

// A failing token read must not strand the waiter: it falls back to counting,
// and the failure is logged once per wait, not on every tick.
func TestInboxReadWait_TokenFails_FallsBackToCountProbeAndLogsOnce(t *testing.T) {
	b := newWatchBackend()
	b.failing.Store(true)
	var logs bytes.Buffer
	svc := NewInboxService(b).WithInboxPolicy(eligibleNow()).WithLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	go func() {
		time.Sleep(900 * time.Millisecond)
		b.add(sqlite.InboxEvent{Seq: 1, Body: "go"})
	}()
	res, err := svc.InboxReadWait(t.Context(), "w", 5*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, 1, strings.Count(logs.String(), "change token unavailable"))
}

// MTIX-326 review S2: eligibility that moves outside the board database
// (operator-local grants, budget windows) changes nothing the token can see.
// A wait holding events must still notice it at the old full-read cadence.
func TestInboxReadWait_HeldEligibilityFlipsWithoutCommit_WakesWithinFullReadFloor(t *testing.T) {
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
		open.Store(true) // no commit: the token never moves
	}()
	began := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 30*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Less(t, time.Since(began), inboxFullReadFloor+inboxReadPollInterval+time.Second)
}

// MTIX-328 acceptance (Claude hook waiter): InboxReadUntil parks without a
// policy too, so an idle hook wait asks the backend nothing after its entry
// read and probe, for longer than five old 250 ms polls.
func TestInboxReadUntil_IdleNoPolicy_NoReadOrCountAfterEntry(t *testing.T) {
	b := newWatchBackend()
	svc := NewInboxService(b)
	res, err := svc.InboxReadUntil(t.Context(), "w", 1300*time.Millisecond, InboxReadOptions{},
		func(r InboxResult) bool { return len(r.Events) > 0 })
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 1, b.listCalls(), "the entry read only")
	assert.Equal(t, 1, b.countCalls(), "the entry probe only")
}

// A held or skipped event keeps the 2 s full read of MTIX-245: the waiter does
// not end (done is false) and re-reads at the floor, not every tick.
func TestInboxReadUntil_PendingButSkipped_ReadsAtTheFloorNotEveryTick(t *testing.T) {
	b := newWatchBackend()
	b.add(sqlite.InboxEvent{Seq: 1, Body: "already delivered"})
	svc := NewInboxService(b)
	_, err := svc.InboxReadUntil(t.Context(), "w", idleWaitLong, InboxReadOptions{},
		func(InboxResult) bool { return false })
	require.NoError(t, err)
	assert.Equal(t, 2, b.listCalls(), "entry read plus one floor read, not ten tick reads")
}
