// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// waitForCancel is a Wait that blocks until the run cancels it, as a real wait does.
func waitForCancel(t *testing.T) func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
	return func(ctx context.Context, _ func(int64) bool) ([]sqlite.InboxEvent, error) {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(10 * time.Second):
			t.Error("the wait was never ended")
			return nil, nil
		}
	}
}

func hookFailureText(t *testing.T, r *hookRig) string {
	t.Helper()
	failures, err := r.log.Failures()
	require.NoError(t, err)
	if len(failures) == 0 {
		return ""
	}
	require.Len(t, failures, 1)
	return failures[0].Error
}

func TestClaudeHook_RenewReturnsAnotherHolder_AbandonsTheFreshTokenAndEndsTheWait(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	r.leases.renewFn = func(context.Context, string) (WakeLease, error) { return WakeLease{Holder: "fresh-token"}, nil }
	r.deps.Wait = waitForCancel(t)

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Zero(t, res.ExitCode)
	assert.Contains(t, hookFailureText(t, r), "renew waiter lease: lease lost")
	r.leases.mu.Lock()
	defer r.leases.mu.Unlock()
	assert.Equal(t, []string{"fresh-token"}, r.leases.abandons, "the fresh token is abandoned and the dead token is not")
}

// stallingLeases runs before once, ahead of the first Renew: the waiter stalled.
type stallingLeases struct {
	ClaudeHookLeases
	once   sync.Once
	before func()
}

func (s *stallingLeases) Renew(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration) (WakeLease, error) {
	s.once.Do(s.before)
	return s.ClaudeHookLeases.Renew(ctx, key, holder, ttl)
}

func TestClaudeHook_VanishedLeaseRowOnRenew_LeavesNoOrphanLease(t *testing.T) {
	store, err := sqlite.New(filepath.Join(t.TempDir(), "board.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	var mu sync.Mutex
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	svc := NewWakeLeaseService(store)
	key := WakeLeaseKey{AgentID: "worker", HostID: "host-1", UID: "501"}
	ttl := time.Minute

	r := newHookRig(t)
	r.deps.Key = key
	r.deps.LeaseTTL = ttl
	r.deps.RenewEvery = 5 * time.Millisecond
	r.deps.Leases = &stallingLeases{ClaudeHookLeases: svc, before: func() {
		// The waiter stalled past its TTL: a contender takes the expired lease and frees it.
		mu.Lock()
		now = now.Add(2 * ttl)
		mu.Unlock()
		taken, aerr := svc.Acquire(t.Context(), key, ttl)
		require.NoError(t, aerr)
		_, aerr = svc.Abandon(t.Context(), key, taken.Holder, ttl)
		require.NoError(t, aerr)
	}}
	r.deps.Wait = waitForCancel(t)

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Zero(t, res.ExitCode)
	assert.Contains(t, hookFailureText(t, r), "lease lost", "the lost lease is recorded for doctor")
	next, err := svc.Acquire(t.Context(), key, ttl)
	require.NoError(t, err, "no orphan lease blocks the next Stop")
	assert.NotEmpty(t, next.Holder)
}

func TestClaudeHook_TransientRenewError_RetriesWhileTheLeaseHolds(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	var calls int
	r.leases.renewFn = func(_ context.Context, holder string) (WakeLease, error) {
		calls++
		if calls <= 3 {
			return WakeLease{}, errors.New("database is busy")
		}
		return WakeLease{Holder: holder}, nil
	}
	var waitErr error
	r.deps.Wait = func(ctx context.Context, _ func(int64) bool) ([]sqlite.InboxEvent, error) {
		require.Eventually(t, func() bool { return r.leases.renewCount() >= 6 }, 5*time.Second, time.Millisecond)
		waitErr = ctx.Err()
		return nil, nil
	}

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookEmpty, res.Outcome)
	assert.NoError(t, waitErr, "a transient renew error does not cancel the wait")
	assert.Empty(t, hookFailureText(t, r))
}

func TestClaudeHook_RenewKeepsFailing_LeaseExpiryEndsTheWaitAndIsRecorded(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	r.deps.LeaseTTL = time.Minute
	var mu sync.Mutex
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	r.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); now = now.Add(25 * time.Second); return now }
	r.leases.renewFn = func(context.Context, string) (WakeLease, error) { return WakeLease{}, errors.New("database is busy") }
	r.deps.Wait = waitForCancel(t)

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Zero(t, res.ExitCode)
	assert.Contains(t, hookFailureText(t, r), "lease expired before a renewal succeeded")
}

func blockingRenew(f *fakeHookLeases, entered chan struct{}, release chan struct{}) {
	var once sync.Once
	f.renewFn = func(_ context.Context, holder string) (WakeLease, error) {
		f.note("renew-start")
		once.Do(func() { close(entered) })
		<-release
		f.note("renew-end")
		return WakeLease{Holder: holder}, nil
	}
}

// runBlocked runs the hook while a Renew is stuck and shows Run waits for it.
func runBlocked(t *testing.T, r *hookRig, entered, release chan struct{}) ClaudeHookResult {
	t.Helper()
	done := make(chan ClaudeHookResult, 1)
	go func() { done <- NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop) }()
	<-entered
	select {
	case <-done:
		t.Fatal("Run returned while a Renew was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	r.leases.mu.Lock()
	assert.Zero(t, r.leases.released, "the lease is not abandoned under an in-flight Renew")
	r.leases.mu.Unlock()
	close(release)
	return <-done
}

func TestClaudeHook_StopJoinsTheRenewerBeforeAbandoning(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = time.Millisecond
	entered, release := make(chan struct{}), make(chan struct{})
	blockingRenew(r.leases, entered, release)
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { <-entered; return nil, nil }

	res := runBlocked(t, r, entered, release)

	assert.Equal(t, ClaudeHookEmpty, res.Outcome)
	assert.Equal(t, []string{"renew-start", "renew-end", "abandon"}, r.leases.order)
}

func TestClaudeHook_PanicInTheWait_StillJoinsTheRenewerBeforeAbandoning(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = time.Millisecond
	entered, release := make(chan struct{}), make(chan struct{})
	blockingRenew(r.leases, entered, release)
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { <-entered; panic("kaboom") }

	res := runBlocked(t, r, entered, release)

	assert.Equal(t, ClaudeHookFailed, res.Outcome)
	assert.Zero(t, res.ExitCode)
	assert.Equal(t, []string{"renew-start", "renew-end", "abandon"}, r.leases.order)
}

func TestClaudeHook_PanicInTheRenewer_ExitsZeroRecordsAndFreesTheLease(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	r.leases.renewFn = func(context.Context, string) (WakeLease, error) { panic("renewer kaboom") }
	r.deps.Wait = waitForCancel(t)

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Zero(t, res.ExitCode, "a panic in the renewer never breaks a session")
	assert.Contains(t, hookFailureText(t, r), "lease renewer panic")
	assert.False(t, r.leases.held, "the lease is still freed")
}

func TestClaudeHook_DefaultRenewCadence_IsAThirdOfTheLeaseTTL(t *testing.T) {
	r := newHookRig(t)
	r.deps.LeaseTTL = 300 * time.Millisecond // RenewEvery unset: every 100 ms
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
		time.Sleep(1050 * time.Millisecond)
		return nil, nil
	}

	NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	got := r.leases.renewCount()
	assert.GreaterOrEqual(t, got, 7, "TTL/3 renews about ten times in 1.05 s, not about three")
	assert.LessOrEqual(t, got, 11)
}
