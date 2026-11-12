// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that an addressed event wakes a session exactly once when the socket route (through
// the dispatcher) and the hook waiter are both armed, including crash windows and the retry
// cap (MTIX-112.40.13, FR-20, ADR-007). Routes are fakes; no live session is involved.
package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

const bothRoutesEvents = 50

var bothRoutesRecipient = model.WakeRecipient{AgentID: "test-agent", HostID: "test-host", UID: "test-uid"}

func bothRoutesKey(seq int64) model.WakeKey { return model.NewWakeKey(bothRoutesRecipient, seq, 1, "") }

// wakeTally counts the wakes each event produced across both routes.
type wakeTally struct {
	mu    sync.Mutex
	wakes map[int64]int
}

func (w *wakeTally) add(seq int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wakes[seq]++
}

// tallySocket is the fake socket route: it counts one wake per Deliver.
type tallySocket struct{ tally *wakeTally }

func (tallySocket) Route() service.WakeRoute { return service.RouteSocketPush }
func (tallySocket) Capabilities(model.WakeKey) service.WakeCapabilities {
	return service.WakeCapabilities{Live: true}
}
func (tallySocket) Probe(context.Context, model.WakeKey) service.WakeHealth {
	return service.WakeHealth{Healthy: true}
}
func (tallySocket) Contract(context.Context) service.ContractCheck {
	return service.ContractCheck{Matches: true}
}
func (a tallySocket) Deliver(_ context.Context, at model.WakeAttempt) (model.WakeResult, error) {
	a.tally.add(at.Key.EventSeq)
	return model.WakeResult{Outcome: model.WakeSent}, nil
}

// freeLeases always grants the waiter lease: each fake hook run is its own session waiter.
type freeLeases struct{}

func (freeLeases) Acquire(context.Context, service.WakeLeaseKey, time.Duration) (service.WakeLease, error) {
	return service.WakeLease{Holder: "h"}, nil
}
func (freeLeases) Renew(context.Context, service.WakeLeaseKey, string, time.Duration) (service.WakeLease, error) {
	return service.WakeLease{Holder: "h"}, nil
}
func (freeLeases) Abandon(context.Context, service.WakeLeaseKey, string, time.Duration) (service.WakeLeaseReleaseResult, error) {
	return service.WakeLeaseReleaseResult{}, nil
}

func hookForEvent(t *testing.T, st *sqlite.Store, seq int64, evidence service.WakeEvidence) *service.ClaudeHook {
	t.Helper()
	return service.NewClaudeHook(service.ClaudeHookDeps{
		Key:      service.WakeLeaseKey{AgentID: bothRoutesRecipient.AgentID, HostID: bothRoutesRecipient.HostID, UID: bothRoutesRecipient.UID},
		Epoch:    func(context.Context) (int64, error) { return 1, nil },
		Wakeable: func(context.Context) (bool, string, error) { return true, "", nil },
		Wait: func(context.Context, func(sqlite.InboxEvent) bool) ([]sqlite.InboxEvent, error) {
			return []sqlite.InboxEvent{{Seq: seq, NodeID: "TEST-1", Author: "sender"}}, nil
		},
		Render:   func([]sqlite.InboxEvent) (string, error) { return "ENVELOPE", nil },
		Leases:   freeLeases{},
		Claims:   st,
		Log:      &service.ClaudeHookLog{Dir: t.TempDir(), Clock: time.Now},
		LeaseTTL: time.Minute, ClaimLease: time.Minute, Evidence: evidence,
	})
}

func bothRoutesDispatcher(t *testing.T, st *sqlite.Store, alerts *service.Alerts, tally *wakeTally, evidence service.WakeEvidence) *service.WakeDispatcher {
	t.Helper()
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: st, Alerts: alerts, Adapters: []service.WakeAdapter{tallySocket{tally}},
		BudgetState: testutil.OperatorState(t), Clock: time.Now, Evidence: evidence,
	})
	require.NoError(t, err)
	return d
}

func TestWakeClaim_BothRoutesArmed_EveryEventWakesExactlyOnce(t *testing.T) {
	// order says who is released first: the dispatcher (socket), the hook waiter, or both at once.
	for _, order := range []string{"socket-first", "hook-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			st, _, alerts := wakeServiceStore(t)
			tally := &wakeTally{wakes: map[int64]int{}}
			d := bothRoutesDispatcher(t, st, alerts, tally, nil)
			startSocket, startHook := make(chan struct{}), make(chan struct{})
			var wg sync.WaitGroup
			var hookFailures atomic.Int32
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startSocket
				for seq := int64(1); seq <= bothRoutesEvents; seq++ {
					if err := d.Pass(t.Context(), []model.WakeKey{bothRoutesKey(seq)}); err != nil {
						hookFailures.Add(1)
					}
				}
			}()
			for seq := int64(1); seq <= bothRoutesEvents; seq++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-startHook
					res := hookForEvent(t, st, seq, nil).Run(t.Context(), service.ClaudeHookStop)
					switch {
					case res.ExitCode == service.ClaudeHookExitWake:
						tally.add(seq)
					case res.Outcome != service.ClaudeHookClaimLost:
						hookFailures.Add(1)
					}
				}()
			}
			switch order {
			case "socket-first":
				close(startSocket)
				time.Sleep(50 * time.Millisecond)
				close(startHook)
			case "hook-first":
				close(startHook)
				time.Sleep(50 * time.Millisecond)
				close(startSocket)
			default:
				close(startSocket)
				close(startHook)
			}
			wg.Wait()
			require.Zero(t, hookFailures.Load(), "no route may fail or be refused")
			duplicates, lost := 0, 0
			for seq := int64(1); seq <= bothRoutesEvents; seq++ {
				switch n := tally.wakes[seq]; {
				case n > 1:
					duplicates++
				case n == 0:
					lost++
				}
			}
			require.Zero(t, duplicates, "an event woke the session more than once")
			require.Zero(t, lost, "an event never woke the session")
			stats, err := st.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.EqualValues(t, bothRoutesEvents, stats.Rows, "one ledger row per event")
			require.Zero(t, stats.Claimed, "every claim reached a terminal outcome")
		})
	}
}

func TestWakeClaim_RetryThroughClaim_NeverWakesASentEvent(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	claimer, err := service.NewWakeClaimer(st, time.Minute, nil, nil)
	require.NoError(t, err)
	attempt, won, err := claimer.Claim(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, st.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}))
	for _, wait := range []time.Duration{time.Second, 2 * time.Minute, 24 * time.Hour} {
		*now = now.Add(wait)
		_, won, err = claimer.Claim(t.Context(), bothRoutesKey(1))
		require.NoError(t, err)
		require.False(t, won, "a retry %s after the send must not wake the event again", wait)
	}
}

func TestWakeClaim_RetryCapIsOne_ThenReportedNotRetried(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	claimer, err := service.NewWakeClaimer(st, time.Minute, nil, nil)
	require.NoError(t, err)
	_, won, err := claimer.Claim(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.True(t, won, "first claim, then the claimant crashes before recording")
	*now = now.Add(2 * time.Minute)
	re, won, err := claimer.Claim(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.True(t, won, "the one permitted retry")
	require.True(t, re.Reclaimed)
	*now = now.Add(2 * time.Minute)
	_, won, err = claimer.Claim(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.False(t, won, "the retry cap is one")
	rows, err := st.WakeStatusRows(t.Context(), "test-host", "test-uid", "test-agent", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, model.WakeUndeliverable, rows[0].Outcome)
	require.Equal(t, model.WakeReasonReclaimCap, rows[0].Reason, "the cap is reported, never silent")
}

func TestWakeClaim_CrashAfterPush_EvidenceSettlesWithoutAnotherWake(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	consulted := 0
	evidence := func(_ context.Context, key model.WakeKey) (bool, error) {
		consulted++
		require.Equal(t, bothRoutesKey(1), key)
		return true, nil
	}
	claimer, err := service.NewWakeClaimer(st, time.Minute, evidence, nil)
	require.NoError(t, err)
	_, won, err := claimer.Claim(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.True(t, won, "pushed, then crashed before recording the result")
	*now = now.Add(2 * time.Minute)
	_, won, err = claimer.Claim(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.False(t, won, "evidence shows the first push landed: no second wake")
	require.Equal(t, 1, consulted)
	got, found, err := st.ReadWakeResult(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, model.WakeConfirmed, got.Outcome)
	require.Equal(t, model.WakeReasonReclaimedAfterCrash, got.Reason)
}

func TestWakeClaim_CrashWithoutEvidence_RedeliversOnceNeverZero(t *testing.T) {
	for name, evidence := range map[string]service.WakeEvidence{
		"no evidence source": nil,
		"evidence says no":   func(context.Context, model.WakeKey) (bool, error) { return false, nil },
		"evidence errors":    func(context.Context, model.WakeKey) (bool, error) { return false, errors.New("stamp unreadable") },
	} {
		t.Run(name, func(t *testing.T) {
			st, now, _ := wakeServiceStore(t)
			claimer, err := service.NewWakeClaimer(st, time.Minute, evidence, nil)
			require.NoError(t, err)
			_, won, err := claimer.Claim(t.Context(), bothRoutesKey(1))
			require.NoError(t, err)
			require.True(t, won)
			*now = now.Add(2 * time.Minute)
			attempt, won, err := claimer.Claim(t.Context(), bothRoutesKey(1))
			require.NoError(t, err)
			require.True(t, won, "never zero wakes: the event is delivered again")
			require.True(t, attempt.Reclaimed)
		})
	}
}

func TestWakeClaim_DispatcherCrashedBeforePush_HookWakesOnceAfterTheLease(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	_, won, err := st.ClaimWake(t.Context(), bothRoutesKey(3), time.Minute)
	require.NoError(t, err)
	require.True(t, won, "the dispatcher claimed, then died before pushing")
	first := hookForEvent(t, st, 3, nil).Run(t.Context(), service.ClaudeHookStop)
	require.Equal(t, service.ClaudeHookClaimLost, first.Outcome, "inside the lease the waiter must not wake")
	*now = now.Add(2 * time.Minute)
	woke := hookForEvent(t, st, 3, nil).Run(t.Context(), service.ClaudeHookStop)
	require.Equal(t, service.ClaudeHookExitWake, woke.ExitCode, "after the lease the waiter delivers the lost event")
	again := hookForEvent(t, st, 3, nil).Run(t.Context(), service.ClaudeHookStop)
	require.Zero(t, again.ExitCode, "and the same event never wakes again")
}

func TestWakeClaim_DispatcherReclaim_UsesEvidenceBeforePushing(t *testing.T) {
	for _, seen := range []bool{true, false} {
		t.Run(map[bool]string{true: "evidence of delivery", false: "no evidence"}[seen], func(t *testing.T) {
			st, now, alerts := wakeServiceStore(t)
			tally := &wakeTally{wakes: map[int64]int{}}
			_, won, err := st.ClaimWake(t.Context(), bothRoutesKey(4), time.Minute)
			require.NoError(t, err)
			require.True(t, won, "an earlier dispatcher claimed and crashed")
			*now = now.Add(2 * time.Minute)
			d := bothRoutesDispatcher(t, st, alerts, tally, func(context.Context, model.WakeKey) (bool, error) { return seen, nil })
			require.NoError(t, d.Pass(t.Context(), []model.WakeKey{bothRoutesKey(4)}))
			if seen {
				require.Zero(t, tally.wakes[4], "the first push landed: no second push")
			} else {
				require.Equal(t, 1, tally.wakes[4], "no evidence: exactly one redelivery")
			}
		})
	}
}
