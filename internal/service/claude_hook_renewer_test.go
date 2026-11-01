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

	"github.com/hyper-swe/mtix/internal/model"

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

// cadenceProbe is a fake ticker: it records the requested interval and lets the
// test deliver ticks by hand, so the cadence is pinned with no wall clock.
type cadenceProbe struct {
	mu       sync.Mutex
	interval time.Duration
	built    int
	stopped  int
	ch       chan time.Time
}

func (p *cadenceProbe) newTicker(d time.Duration) (<-chan time.Time, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interval, p.built = d, p.built+1
	p.ch = make(chan time.Time)
	return p.ch, func() { p.mu.Lock(); p.stopped++; p.mu.Unlock() }
}

func (p *cadenceProbe) snapshot() (time.Duration, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interval, p.built, p.stopped
}

// runCadence runs the hook with a probe ticker and delivers n ticks, waiting for each renew.
func runCadence(t *testing.T, r *hookRig, n int) *cadenceProbe {
	t.Helper()
	p := &cadenceProbe{}
	r.deps.NewTicker = p.newTicker
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
		// The renewer goroutine builds its ticker asynchronously: wait for it.
		require.Eventually(t, func() bool { _, built, _ := p.snapshot(); return built == 1 }, 10*time.Second, time.Millisecond)
		for i := 1; i <= n; i++ {
			p.mu.Lock()
			ch := p.ch
			p.mu.Unlock()
			select {
			case ch <- time.Time{}:
			case <-time.After(10 * time.Second):
				t.Error("the renewer never took the tick")
				return nil, nil
			}
			require.Eventually(t, func() bool { return r.leases.renewCount() >= i }, 10*time.Second, time.Millisecond)
		}
		return nil, nil
	}
	NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)
	return p
}

func TestClaudeHook_DefaultRenewCadence_IsAThirdOfTheLeaseTTL(t *testing.T) {
	r := newHookRig(t)
	r.deps.LeaseTTL = 300 * time.Millisecond // RenewEvery unset: the ticker interval is LeaseTTL/3

	p := runCadence(t, r, 5)

	interval, built, stopped := p.snapshot()
	assert.Equal(t, 100*time.Millisecond, interval, "TTL/2 or TTL/4 would not be exactly a third")
	assert.Equal(t, 1, built, "one ticker serves the whole wait")
	assert.Equal(t, 1, stopped, "the ticker is stopped when the wait ends")
	assert.Equal(t, 5, r.leases.renewCount(), "the renewer renews once per tick")
}

func TestClaudeHook_ExplicitRenewEvery_OverridesTheThirdOfTheTTL(t *testing.T) {
	r := newHookRig(t)
	r.deps.LeaseTTL = 300 * time.Millisecond
	r.deps.RenewEvery = 40 * time.Millisecond

	p := runCadence(t, r, 2)

	interval, _, _ := p.snapshot()
	assert.Equal(t, 40*time.Millisecond, interval)
}

func TestClaudeHook_SuccessfulRenewal_RefreshesTheRetryWindow(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	r.deps.LeaseTTL = time.Minute
	var mu sync.Mutex
	cur := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	t0 := cur
	r.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	set := func(d time.Duration) { mu.Lock(); cur = t0.Add(d); mu.Unlock() }
	calls := 0
	r.leases.renewFn = func(_ context.Context, holder string) (WakeLease, error) {
		calls++
		switch calls {
		case 1:
			set(50 * time.Second) // a success late in the lease: the window restarts here
			return WakeLease{Holder: holder}, nil
		case 2:
			set(80 * time.Second) // 80 s after the start but only 30 s after the last success
			return WakeLease{}, errors.New("database is busy")
		}
		set(85 * time.Second)
		return WakeLease{Holder: holder}, nil
	}
	var waitErr error
	r.deps.Wait = func(ctx context.Context, _ func(int64) bool) ([]sqlite.InboxEvent, error) {
		require.Eventually(t, func() bool { return r.leases.renewCount() >= 5 }, 5*time.Second, time.Millisecond)
		waitErr = ctx.Err()
		return nil, nil
	}

	NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.NoError(t, waitErr, "the busy error is retried because the window runs from the last success")
	assert.Empty(t, hookFailureText(t, r))
}

func TestClaudeHook_CancelDuringARenewThatReCreatesTheLease_AbandonsTheFreshToken(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	entered := make(chan struct{})
	var once sync.Once
	r.leases.renewFn = func(ctx context.Context, _ string) (WakeLease, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done() // the run ends while the Renew is in flight; the Renew still commits
		return WakeLease{Holder: "fresh-token"}, nil
	}
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { <-entered; return nil, nil }

	NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	r.leases.mu.Lock()
	defer r.leases.mu.Unlock()
	assert.Equal(t, []string{"fresh-token"}, r.leases.abandons, "the re-created lease is freed, not orphaned")
}

func TestClaudeHook_LeaseLostReportedAfterCancel_IsNotRecordedAsAFailure(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	entered := make(chan struct{})
	var once sync.Once
	r.leases.renewFn = func(ctx context.Context, _ string) (WakeLease, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return WakeLease{}, model.ErrAlreadyClaimed // the run is over: this is not a lost lease
	}
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { <-entered; return nil, nil }

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookEmpty, res.Outcome)
	assert.Empty(t, hookFailureText(t, r), "a Renew that finishes after the run ended is ignored")
	assert.Equal(t, 1, r.leases.released, "the run still frees its own lease")
}
