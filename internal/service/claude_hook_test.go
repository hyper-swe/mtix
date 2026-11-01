// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// fakeHookLeases is a single-holder lease with the store's contention rule.
type fakeHookLeases struct {
	mu       sync.Mutex
	held     bool
	acquired int
	released int
	relErr   error
	renewals []string // holder tokens passed to Renew
	renewErr error
	relaunch int // Release calls: the store rearms a lease when mail arrived during the wait
}

func (f *fakeHookLeases) Acquire(_ context.Context, _ WakeLeaseKey, _ time.Duration) (WakeLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		return WakeLease{}, model.ErrAlreadyClaimed
	}
	f.held = true
	f.acquired++
	return WakeLease{Holder: "holder-1"}, nil
}

// Abandon frees the lease whatever is pending, as the store does.
func (f *fakeHookLeases) Abandon(_ context.Context, _ WakeLeaseKey, holder string, _ time.Duration) (WakeLeaseReleaseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = false
	f.released++
	if holder != "holder-1" {
		return WakeLeaseReleaseResult{}, errors.New("wrong holder")
	}
	return WakeLeaseReleaseResult{}, f.relErr
}

// Release models the store's relaunch outcome: the lease stays held.
func (f *fakeHookLeases) Release(_ context.Context, _ WakeLeaseKey, _ string, _ time.Duration) (WakeLeaseReleaseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relaunch++
	return WakeLeaseReleaseResult{Outcome: "relaunch"}, nil
}

func (f *fakeHookLeases) Renew(_ context.Context, _ WakeLeaseKey, holder string, _ time.Duration) (WakeLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewals = append(f.renewals, holder)
	if f.renewErr != nil {
		return WakeLease{}, f.renewErr
	}
	return WakeLease{Holder: holder}, nil
}

func (f *fakeHookLeases) renewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.renewals)
}

// fakeHookClaims claims each event once and records outcomes.
type fakeHookClaims struct {
	mu       sync.Mutex
	claimed  map[int64]bool
	outcomes map[int64]model.WakeOutcome
	epochs   []int64
	claimErr error
	failSeq  int64 // ClaimWake fails for this event only (0 = off)
	recErr   error // RecordWakeOutcome fails
}

func newFakeHookClaims() *fakeHookClaims {
	return &fakeHookClaims{claimed: map[int64]bool{}, outcomes: map[int64]model.WakeOutcome{}}
}

func (f *fakeHookClaims) ClaimWake(_ context.Context, key model.WakeKey, _ time.Duration) (model.WakeAttempt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return model.WakeAttempt{}, false, f.claimErr
	}
	if f.failSeq != 0 && key.EventSeq == f.failSeq {
		return model.WakeAttempt{}, false, errors.New("claim store down")
	}
	f.epochs = append(f.epochs, key.Epoch)
	if f.claimed[key.EventSeq] {
		return model.WakeAttempt{}, false, nil
	}
	f.claimed[key.EventSeq] = true
	return model.WakeAttempt{Key: key, Token: "t"}, true, nil
}

func (f *fakeHookClaims) RecordWakeOutcome(_ context.Context, a model.WakeAttempt, o model.WakeOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recErr != nil {
		return f.recErr
	}
	f.outcomes[a.Key.EventSeq] = o
	return nil
}

type hookRig struct {
	deps       ClaudeHookDeps
	leases     *fakeHookLeases
	claims     *fakeHookClaims
	log        *ClaudeHookLog
	registered int
}

func newHookRig(t *testing.T, events ...sqlite.InboxEvent) *hookRig {
	t.Helper()
	r := &hookRig{leases: &fakeHookLeases{}, claims: newFakeHookClaims()}
	r.log = &ClaudeHookLog{Dir: t.TempDir(), Clock: func() time.Time { return time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC) }}
	r.deps = ClaudeHookDeps{
		Key:      WakeLeaseKey{AgentID: "worker", HostID: "host-1", UID: "501"},
		Register: func(context.Context) error { r.registered++; return nil },
		Epoch:    func(context.Context) (int64, error) { return 7, nil },
		Wakeable: func(context.Context) (bool, string, error) { return true, "implicit local registration", nil },
		Wait:     func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { return events, nil },
		Render: func(es []sqlite.InboxEvent) (string, error) {
			out := "ENVELOPE"
			for _, e := range es {
				out += " " + e.Body
			}
			return out, nil
		},
		Leases: r.leases, Claims: r.claims, Log: r.log,
		LeaseTTL: time.Minute, ClaimLease: time.Minute,
	}
	return r
}

func hookEvent(seq int64, body string) sqlite.InboxEvent {
	return sqlite.InboxEvent{Seq: seq, NodeID: "TEST-1", Author: "sender", Body: body}
}

func TestClaudeHook_Stop_PendingEvent_ExitsTwoWithEnvelope(t *testing.T) {
	r := newHookRig(t, hookEvent(11, "please review"))
	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookExitWake, res.ExitCode)
	assert.Equal(t, "ENVELOPE please review", res.Envelope)
	assert.Equal(t, ClaudeHookWoke, res.Outcome)
	assert.Equal(t, model.WakeDelivered, r.claims.outcomes[11], "the claim is settled as delivered")
	assert.Equal(t, []int64{7}, r.claims.epochs, "the claim carries the registration epoch")
	assert.Equal(t, 1, r.leases.released, "the waiter lease is released after the run")
}

func TestClaudeHook_SessionStart_RegistersBeforeArming(t *testing.T) {
	r := newHookRig(t)
	order := []string{}
	r.deps.Register = func(context.Context) error { order = append(order, "register"); return nil }
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
		order = append(order, "wait")
		return nil, nil
	}

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookSessionStart)

	assert.Equal(t, []string{"register", "wait"}, order)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, ClaudeHookEmpty, res.Outcome)
	assert.Equal(t, 1, r.leases.released)
}

func TestClaudeHook_Stop_DoesNotRegister(t *testing.T) {
	r := newHookRig(t)
	NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)
	assert.Zero(t, r.registered)
}

func TestClaudeHook_SecondStartWhileArmed_ReportsAlreadyArmedAndWaitsOnce(t *testing.T) {
	r := newHookRig(t, hookEvent(1, "a"))
	var waits int
	var mu sync.Mutex
	entered, release := make(chan struct{}), make(chan struct{})
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
		mu.Lock()
		waits++
		mu.Unlock()
		close(entered)
		<-release
		return nil, nil
	}
	hook := NewClaudeHook(r.deps)
	first := make(chan ClaudeHookResult, 1)
	go func() { first <- hook.Run(t.Context(), ClaudeHookSessionStart) }()
	<-entered

	second := hook.Run(t.Context(), ClaudeHookSessionStart)
	close(release)
	<-first

	assert.Equal(t, ClaudeHookAlreadyArmed, second.Outcome)
	assert.Equal(t, 0, second.ExitCode)
	assert.Equal(t, 1, waits, "a double SessionStart yields one waiter")
	assert.Equal(t, 1, r.leases.acquired)
}

func TestClaudeHook_WakePolicyHolds_NoLeaseNoWait(t *testing.T) {
	for _, reason := range []string{"denied", "quarantined", "unconfirmed", "not_registered"} {
		t.Run(reason, func(t *testing.T) {
			r := newHookRig(t, hookEvent(1, "a"))
			r.deps.Wakeable = func(context.Context) (bool, string, error) { return false, reason, nil }
			r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
				t.Fatal("a held agent must not wait")
				return nil, nil
			}

			res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

			assert.Equal(t, ClaudeHookHeld, res.Outcome)
			assert.Equal(t, reason, res.Detail)
			assert.Zero(t, res.ExitCode)
			assert.Zero(t, r.leases.acquired)
		})
	}
}

func TestClaudeHook_EventClaimedByAnotherRoute_ExitsZeroNoDuplicateWake(t *testing.T) {
	r := newHookRig(t, hookEvent(5, "old"), hookEvent(6, "new"))
	r.claims.claimed[5] = true // the dispatcher already won event 5

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookExitWake, res.ExitCode)
	assert.Equal(t, "ENVELOPE new", res.Envelope, "only the event this run claimed is delivered")
	_, settled := r.claims.outcomes[5]
	assert.False(t, settled, "an event this run did not claim is never settled by it")

	again := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)
	assert.Equal(t, ClaudeHookClaimLost, again.Outcome)
	assert.Zero(t, again.ExitCode, "the same event never wakes the session twice")
}

func TestClaudeHook_InternalErrors_ExitZeroAndRecordedForDoctor(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		breakIt func(*hookRig)
		event   ClaudeHookEvent
	}{
		{"register", func(r *hookRig) { r.deps.Register = func(context.Context) error { return boom } }, ClaudeHookSessionStart},
		{"policy", func(r *hookRig) {
			r.deps.Wakeable = func(context.Context) (bool, string, error) { return false, "", boom }
		}, ClaudeHookStop},
		{"wait", func(r *hookRig) {
			r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { return nil, boom }
		}, ClaudeHookStop},
		{"epoch", func(r *hookRig) { r.deps.Epoch = func(context.Context) (int64, error) { return 0, boom } }, ClaudeHookStop},
		{"claim", func(r *hookRig) { r.claims.claimErr = boom }, ClaudeHookStop},
		{"render", func(r *hookRig) { r.deps.Render = func([]sqlite.InboxEvent) (string, error) { return "", boom } }, ClaudeHookStop},
		{"panic", func(r *hookRig) {
			r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) { panic("kaboom") }
		}, ClaudeHookStop},
		{"unknown event", func(*hookRig) {}, ClaudeHookEvent("nope")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newHookRig(t, hookEvent(1, "a"))
			tt.breakIt(r)

			res := NewClaudeHook(r.deps).Run(t.Context(), tt.event)

			assert.Zero(t, res.ExitCode, "a hook never breaks a session")
			assert.Equal(t, ClaudeHookFailed, res.Outcome)
			failures, err := r.log.Failures()
			require.NoError(t, err)
			require.Len(t, failures, 1)
			assert.Equal(t, "worker", failures[0].Agent)
			assert.Equal(t, tt.event, failures[0].Event)
			assert.False(t, r.leases.held, "the lease is released on every path")
		})
	}
}

func TestClaudeHook_SuccessAfterFailure_ClearsTheRecord(t *testing.T) {
	r := newHookRig(t)
	fail := true
	r.deps.Wait = func(context.Context, func(int64) bool) ([]sqlite.InboxEvent, error) {
		if fail {
			return nil, errors.New("transient")
		}
		return nil, nil
	}
	hook := NewClaudeHook(r.deps)
	hook.Run(t.Context(), ClaudeHookStop)
	failures, err := r.log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1)

	fail = false
	hook.Run(t.Context(), ClaudeHookStop)
	failures, err = r.log.Failures()
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestClaudeHook_LeaseReleaseFails_StillWakesAndRecordsForDoctor(t *testing.T) {
	r := newHookRig(t, hookEvent(3, "x"))
	r.leases.relErr = errors.New("release boom")
	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)
	assert.Equal(t, ClaudeHookExitWake, res.ExitCode, "a failed release never cancels a settled wake")
	assert.Equal(t, ClaudeHookWoke, res.Outcome)
	failures, err := r.log.Failures()
	require.NoError(t, err)
	assert.Len(t, failures, 1)
}

func TestClaudeHook_LongWait_RenewsTheLeaseWithItsHolderToken(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	r.deps.Wait = func(_ context.Context, _ func(int64) bool) ([]sqlite.InboxEvent, error) {
		require.Eventually(t, func() bool { return r.leases.renewCount() >= 3 }, 5*time.Second, time.Millisecond)
		return nil, nil
	}

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookEmpty, res.Outcome)
	r.leases.mu.Lock()
	defer r.leases.mu.Unlock()
	for _, holder := range r.leases.renewals {
		assert.Equal(t, "holder-1", holder, "the renewal presents the token the lease was acquired with")
	}
}

func TestClaudeHook_LeaseLostDuringWait_EndsTheWaitAndRecordsForDoctor(t *testing.T) {
	r := newHookRig(t)
	r.deps.RenewEvery = 5 * time.Millisecond
	r.leases.renewErr = model.ErrAlreadyClaimed
	r.deps.Wait = func(ctx context.Context, _ func(int64) bool) ([]sqlite.InboxEvent, error) {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(5 * time.Second):
			t.Error("a lost lease must end the wait")
			return nil, nil
		}
	}

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Zero(t, res.ExitCode)
	failures, err := r.log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Error, "renew waiter lease")
}

func TestClaudeHook_Run_FreesTheLeaseWithoutRearmingOnEveryOutcome(t *testing.T) {
	r := newHookRig(t, hookEvent(11, "mail that arrived during the wait"))

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookWoke, res.Outcome)
	assert.Zero(t, r.leases.relaunch, "a one-shot hook never rearms its lease")
	assert.False(t, r.leases.held, "the next Stop can arm a new waiter")
}
