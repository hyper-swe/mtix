// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests stalled-turn recovery on the hook route (MTIX-112.40.12, FR-20): the stall rule
// (T = wake.confirm_window from the event, no timer), recovery through the single
// WakeClaimer with a retry cap of one, and the simulated never-ending turn with a push
// route live and with the hook route alone. Routes are fakes; no live session is involved.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

const stallWindow = 30 * time.Second

func TestEventStalled_Table(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	old, fresh := now.Add(-2*stallWindow), now.Add(-stallWindow/2)
	tests := []struct {
		name string
		in   service.StallInput
		want bool
	}{
		{"sent past the window and no waiter", service.StallInput{HasRow: true, Outcome: model.WakeSent, RowAt: old}, true},
		{"legacy delivered row reads as sent", service.StallInput{HasRow: true, Outcome: model.WakeDelivered, RowAt: old}, true},
		{"sent within the window", service.StallInput{HasRow: true, Outcome: model.WakeSent, RowAt: fresh}, false},
		{"sent past the window but a waiter is armed", service.StallInput{HasRow: true, Outcome: model.WakeSent, RowAt: old, WaiterLive: true}, false},
		{"confirmed", service.StallInput{HasRow: true, Outcome: model.WakeConfirmed, RowAt: old}, false},
		{"in-flight claim", service.StallInput{HasRow: true, Outcome: model.WakeClaimed, RowAt: old}, false},
		{"closed undeliverable is reported elsewhere", service.StallInput{HasRow: true, Outcome: model.WakeUndeliverable, RowAt: old}, false},
		{"no row, event older than the window, no waiter", service.StallInput{EventAt: old}, true},
		{"no row, event within the window", service.StallInput{EventAt: fresh}, false},
		{"no row, old event, waiter armed", service.StallInput{EventAt: old, WaiterLive: true}, false},
		{"exactly at the window is not yet stalled", service.StallInput{EventAt: now.Add(-stallWindow)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.Now, in.Window = now, stallWindow
			require.Equal(t, tt.want, service.EventStalled(in))
		})
	}
}

func TestEventStalled_NonPositiveWindow_NeverStalls(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	require.False(t, service.EventStalled(service.StallInput{EventAt: now.Add(-time.Hour), Now: now}))
}

// allowAdmission is a budget admission that allows every claim, so the claimer tests judge
// the stall rule and the cap alone.
func allowAdmission(_ context.Context, _ model.WakeKey, reopen func(service.WakeAdmitFunc) (model.WakeBudgetResult, error)) (model.WakeAttempt, bool, error) {
	res, err := reopen(func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	})
	return res.Attempt, res.Won, err
}

func stallClaimer(t *testing.T, st *sqlite.Store) *service.WakeClaimer {
	t.Helper()
	c, err := service.NewWakeClaimer(st, time.Minute, service.InboxAckEvidence(st), nil)
	require.NoError(t, err)
	return c
}

func TestWakeClaimerReclaimStalled_ReopensOnceAcknowledgedIsConfirmedAndCapHolds(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	seq := addressedSeq(t, st)
	key := bothRoutesKey(seq)
	c := stallClaimer(t, st)
	attempt, won, err := c.Claim(t.Context(), key)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, st.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}))

	_, won, err = c.ReclaimStalled(t.Context(), key, stallWindow, allowAdmission)
	require.NoError(t, err)
	require.False(t, won, "within the window nothing is reopened")

	*now = now.Add(2 * stallWindow)
	re, won, err := c.ReclaimStalled(t.Context(), key, stallWindow, allowAdmission)
	require.NoError(t, err)
	require.True(t, won, "unacknowledged and past the window: one redelivery")
	require.True(t, re.Reclaimed)
	require.NoError(t, st.RecordWakeResult(t.Context(), re, model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}))

	*now = now.Add(2 * stallWindow)
	_, won, err = c.ReclaimStalled(t.Context(), key, stallWindow, allowAdmission)
	require.NoError(t, err)
	require.False(t, won, "the retry cap is one")
	got, _, err := st.ReadWakeResult(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeUndeliverable, got.Outcome)
	require.Equal(t, model.WakeReasonStallUnconfirmed, got.Reason)
}

func TestWakeClaimerReclaimStalled_AcknowledgedEvent_SettlesConfirmedWithoutRedelivery(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	seq := addressedSeq(t, st)
	key := bothRoutesKey(seq)
	c := stallClaimer(t, st)
	attempt, _, err := c.Claim(t.Context(), key)
	require.NoError(t, err)
	require.NoError(t, st.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}))
	require.NoError(t, st.InboxAck(t.Context(), bothRoutesRecipient.AgentID, seq), "the stalled turn recovered and read the message")
	*now = now.Add(2 * stallWindow)
	_, won, err := c.ReclaimStalled(t.Context(), key, stallWindow, allowAdmission)
	require.NoError(t, err)
	require.False(t, won)
	got, _, err := st.ReadWakeResult(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeConfirmed, got.Outcome)
}

func TestWakeClaimerReclaimStalled_LedgerWithoutStallSupport_ReturnsInvalidInput(t *testing.T) {
	c, err := service.NewWakeClaimer(bareLedger{}, time.Minute, nil, nil)
	require.NoError(t, err)
	_, _, err = c.ReclaimStalled(t.Context(), bothRoutesKey(1), stallWindow, allowAdmission)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

type bareLedger struct{}

func (bareLedger) ClaimWake(context.Context, model.WakeKey, time.Duration) (model.WakeAttempt, bool, error) {
	return model.WakeAttempt{}, false, nil
}
func (bareLedger) RecordWakeResult(context.Context, model.WakeAttempt, model.WakeResult) error {
	return nil
}
func (bareLedger) UpgradeWakeConfirmed(context.Context, model.WakeKey) error { return nil }

// offRoute is a registered adapter that is not live, so the registration is hook-only.
type offRoute struct{ tallySocket }

func (offRoute) Capabilities(model.WakeKey) service.WakeCapabilities {
	return service.WakeCapabilities{Live: false}
}

func stallDispatcher(t *testing.T, st *sqlite.Store, alerts *service.Alerts, adapter service.WakeAdapter, waiterLive bool) *service.WakeDispatcher {
	t.Helper()
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: st, Alerts: alerts, Adapters: []service.WakeAdapter{adapter},
		BudgetState: testutil.OperatorState(t), Clock: time.Now,
		Stall: &service.WakeStallDeps{
			Window:     func(string) (time.Duration, error) { return stallWindow, nil },
			WaiterLive: func(context.Context, model.WakeKey) (bool, error) { return waiterLive, nil },
		},
	})
	require.NoError(t, err)
	return d
}

// stallEvents is the hook-only event count; pushStallEvents stays under the default
// wake.backoff_after_unacked of 5 so the push route is never held by the back-off guard.
const (
	stallEvents     = 30
	pushStallEvents = 4
)

// stallHookWake plays the hook waiter waking the session for event 1: it claims and exits 2.
func stallHookWake(t *testing.T, st *sqlite.Store, tally *wakeTally) {
	t.Helper()
	res := hookForEvent(t, st, 1, nil).Run(t.Context(), service.ClaudeHookStop)
	require.Equal(t, service.ClaudeHookExitWake, res.ExitCode)
	tally.add(1)
}

func TestNeverEndingTurn_PushRouteLive_StalledEventDeliveredOnceAndCapped(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := stallDispatcher(t, st, alerts, tallySocket{tally}, false)
	stallHookWake(t, st, tally) // wake 1 starts a turn that never reaches Stop: no waiter is ever armed again
	keys := make([]model.WakeKey, 0, pushStallEvents)
	for seq := int64(1); seq <= pushStallEvents; seq++ {
		keys = append(keys, bothRoutesKey(seq))
	}
	require.NoError(t, d.Pass(t.Context(), keys), "events 2..N reach the push route on the normal pass")
	for seq := int64(2); seq <= pushStallEvents; seq++ {
		require.Equal(t, 1, tally.wakes[seq], "event %d: exactly one wake", seq)
	}
	require.Equal(t, 1, tally.wakes[1], "within the window nothing is redelivered")

	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), keys))
	require.Equal(t, 2, tally.wakes[1], "the stalled event is redelivered exactly once through the push route")
	for seq := int64(2); seq <= pushStallEvents; seq++ {
		require.NotEqual(t, 0, tally.wakes[seq], "event %d is never lost", seq)
	}

	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), keys))
	require.Equal(t, 2, tally.wakes[1], "the retry cap is one: a second stall pushes nothing")
	got, _, err := st.ReadWakeResult(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.Equal(t, model.WakeUndeliverable, got.Outcome)
	require.Equal(t, model.WakeReasonStallUnconfirmed, got.Reason, "reported, not retried")
}

func TestNeverEndingTurn_PushRouteLive_WaiterArmedAgain_NothingRedelivered(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := stallDispatcher(t, st, alerts, tallySocket{tally}, true) // a Stop re-armed the waiter: the turn was only slow
	stallHookWake(t, st, tally)
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{bothRoutesKey(1)}))
	require.Equal(t, 1, tally.wakes[1])
}

func stallStatusService(st *sqlite.Store, now *time.Time, waiterLive bool, pending []service.PendingEvent) *service.WakeStatusService {
	return service.NewWakeStatusService(service.WakeStatusDeps{
		Settings:   func(string) (bool, time.Duration, error) { return true, stallWindow, nil },
		Registered: func(context.Context, string) (bool, error) { return true, nil },
		Dispatcher: func(context.Context) (bool, error) { return true, nil },
		Rows: func(ctx context.Context, agent string, limit int) ([]model.WakeStatusRow, error) {
			return st.WakeStatusRows(ctx, bothRoutesRecipient.HostID, bothRoutesRecipient.UID, agent, limit)
		},
		Pending:    func(context.Context, string) ([]service.PendingEvent, error) { return pending, nil },
		WaiterLive: func(context.Context, string) (bool, error) { return waiterLive, nil },
		Clock:      func() time.Time { return *now },
	})
}

func TestNeverEndingTurn_HookOnly_ReportedStalledNeverLostAndLaterStopWaiterClaims(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := stallDispatcher(t, st, alerts, offRoute{tallySocket{tally}}, false)
	stallHookWake(t, st, tally)
	start := *now
	pending := make([]service.PendingEvent, 0, stallEvents)
	keys := make([]model.WakeKey, 0, stallEvents)
	for seq := int64(1); seq <= stallEvents; seq++ {
		pending = append(pending, service.PendingEvent{Seq: seq, At: start})
		keys = append(keys, bothRoutesKey(seq))
	}
	svc := stallStatusService(st, now, false, pending)
	require.NotEqual(t, model.WakeStateStalled, svc.Current(t.Context(), "test-agent").State, "within the window nothing is stalled")

	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), keys[:1]), "no push route: recovery must not error and must not write a row")
	require.Equal(t, 1, tally.wakes[1], "nothing is pushed without a route")
	got, _, err := st.ReadWakeResult(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.Equal(t, model.WakeSent, got.Outcome, "hook-only writes no terminal ledger row")
	for seq := int64(2); seq <= stallEvents; seq++ {
		_, found, err := st.ReadWakeResult(t.Context(), bothRoutesKey(seq))
		require.NoError(t, err)
		require.False(t, found, "event %d has no ledger row, so a Stop waiter can still claim it", seq)
	}

	cur := svc.Current(t.Context(), "test-agent")
	require.Equal(t, model.WakeStateStalled, cur.State)
	require.Equal(t, model.WakeReasonWaiterNotArmed, cur.Reason)
	require.NotEmpty(t, cur.Fix)
	recent, err := svc.Recent(t.Context(), "test-agent", 100)
	require.NoError(t, err)
	stalled := 0
	for _, r := range recent {
		if r.State == model.WakeStateStalled {
			stalled++
		}
	}
	require.Equal(t, stallEvents, stalled, "every unconfirmed event is reported stalled, none lost")

	for seq := int64(2); seq <= stallEvents; seq++ { // the stalled turn ends: Stop re-arms and the waiter claims each event
		res := hookForEvent(t, st, seq, nil).Run(t.Context(), service.ClaudeHookStop)
		require.Equal(t, service.ClaudeHookExitWake, res.ExitCode, "event %d is still claimable by the later Stop waiter", seq)
	}
}

func TestStatusStalled_WaiterArmedOrAcknowledged_IsNotStalled(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	pending := []service.PendingEvent{{Seq: 5, At: *now}}
	*now = now.Add(2 * stallWindow)
	require.NotEqual(t, model.WakeStateStalled, stallStatusService(st, now, true, pending).Current(t.Context(), "test-agent").State)
	require.NotEqual(t, model.WakeStateStalled, stallStatusService(st, now, false, nil).Current(t.Context(), "test-agent").State, "an acknowledged event is not pending")
}

func TestListenerDoctor_StalledRow_FailsWithFixAndData(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	pending := []service.PendingEvent{{Seq: 5, At: *now}}
	*now = now.Add(2 * stallWindow)
	deps := service.WakeListenerDoctorDeps{
		Agents:     func(context.Context) ([]string, error) { return []string{"test-agent"}, nil },
		Dispatcher: func(context.Context) (bool, error) { return true, nil },
		Status:     stallStatusService(st, now, false, pending),
		Clock:      func() time.Time { return *now },
	}
	var row service.DoctorCheck
	for _, c := range service.WakeListenerDoctorChecks(deps) {
		if c.ID == "wake.listener.stalled" {
			row = c
		}
	}
	require.NotNil(t, row.Run, "the stalled row exists")
	res := row.Run(t.Context())
	require.Equal(t, service.DoctorWarn, res.Status)
	require.NotEmpty(t, res.Fix)
	require.Contains(t, res.Detail, "wake.confirm_window")
	data, ok := res.Data["agents"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{"test-agent"}, data)

	deps.Status = stallStatusService(st, now, true, pending)
	for _, c := range service.WakeListenerDoctorChecks(deps) {
		if c.ID == "wake.listener.stalled" {
			require.Equal(t, service.DoctorInfo, c.Run(t.Context()).Status)
		}
	}
}
