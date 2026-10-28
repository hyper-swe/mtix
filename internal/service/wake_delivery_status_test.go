// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the sender-visible wake delivery status (MTIX-112.40.6). Ledger-row states
// (nudged, unconfirmed, confirmed, held, undeliverable) are SEEDED-ONLY until the wake
// scanner (PR 256) writes rows in production; they are exercised here with seeded rows.
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

var statusNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type statusFixture struct {
	live                                  bool
	window                                time.Duration
	registered                            bool
	dispatcher                            bool
	rows                                  []model.WakeStatusRow
	settingsErr, regErr, dispErr, rowsErr error
}

func (f *statusFixture) svc() *service.WakeStatusService {
	return service.NewWakeStatusService(service.WakeStatusDeps{
		Settings:   func(string) (bool, time.Duration, error) { return f.live, f.window, f.settingsErr },
		Registered: func(context.Context, string) (bool, error) { return f.registered, f.regErr },
		Dispatcher: func(context.Context) (bool, error) { return f.dispatcher, f.dispErr },
		Rows:       func(context.Context, string, int) ([]model.WakeStatusRow, error) { return f.rows, f.rowsErr },
		Clock:      func() time.Time { return statusNow },
	})
}

func okFixture() *statusFixture {
	return &statusFixture{live: true, window: 30 * time.Second, registered: true, dispatcher: true}
}

func TestWakeStatusCurrent_ReachableStates(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		mutate func(*statusFixture)
		want   model.WakeState
		reason string
	}{
		{"all healthy is queued", func(*statusFixture) {}, model.WakeStateQueued, ""},
		{"live wake off", func(f *statusFixture) { f.live = false }, model.WakeStateOff, ""},
		{"off wins over everything else", func(f *statusFixture) { f.live = false; f.registered = false }, model.WakeStateOff, ""},
		{"not registered", func(f *statusFixture) { f.registered = false }, model.WakeStateNoLiveSession, ""},
		{"no dispatcher", func(f *statusFixture) { f.dispatcher = false }, model.WakeStateNoDispatcher, ""},
		{"settings unreadable is unknown", func(f *statusFixture) { f.settingsErr = boom }, model.WakeStateUnknown, "settings: boom"},
		{"registration unreadable is unknown", func(f *statusFixture) { f.regErr = boom }, model.WakeStateUnknown, "registration: boom"},
		{"dispatcher unreadable is unknown", func(f *statusFixture) { f.dispErr = boom }, model.WakeStateUnknown, "dispatcher: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := okFixture()
			tt.mutate(f)
			got := f.svc().Current(t.Context(), "worker")
			require.Equal(t, tt.want, got.State)
			require.Equal(t, tt.reason, got.Reason)
			require.NotEmpty(t, got.Fix, "every state names a fix or next step")
			require.True(t, got.State != "")
		})
	}
}

func TestWakeStatusRecent_SeededRows_MapEveryOutcome(t *testing.T) {
	ago := func(d time.Duration) string { return statusNow.Add(-d).Format(time.RFC3339Nano) }
	f := okFixture()
	f.rows = []model.WakeStatusRow{
		{EventSeq: 7, Outcome: model.WakeSent, Route: "hook-waiter", OutcomeAt: ago(5 * time.Second), ClaimedAt: ago(6 * time.Second)},
		{EventSeq: 6, Outcome: model.WakeSent, Route: "hook-waiter", OutcomeAt: ago(2 * time.Minute), ClaimedAt: ago(2 * time.Minute)},
		{EventSeq: 5, Outcome: model.WakeConfirmed, Route: "claude-socket", OutcomeAt: ago(time.Hour)},
		{EventSeq: 4, Outcome: model.WakeHeld, Route: "claude-socket", OutcomeAt: ago(time.Hour)},
		{EventSeq: 3, Outcome: model.WakeUndeliverable, Reason: "no-live-route", Route: "inbox-only", OutcomeAt: ago(time.Hour)},
		{EventSeq: 2, Outcome: model.WakeDelivered, ClaimedAt: ago(10 * time.Minute)},
		{EventSeq: 1, Outcome: model.WakeError, ClaimedAt: ago(10 * time.Minute)},
	}
	got, err := f.svc().Recent(t.Context(), "worker", 10)
	require.NoError(t, err)
	states := make([]model.WakeState, len(got))
	for i, g := range got {
		states[i] = g.State
	}
	require.Equal(t, []model.WakeState{
		model.WakeStateNudged, model.WakeStateUnconfirmed, model.WakeStateConfirmed, model.WakeStateHeld,
		model.WakeStateUndeliverable, model.WakeStateUnconfirmed, model.WakeStateUndeliverable,
	}, states)
	require.Equal(t, int64(7), got[0].EventSeq)
	require.Equal(t, "hook-waiter", got[0].Route)
	require.Equal(t, int64(5), got[0].AgeSeconds)
	require.Equal(t, "no-live-route", got[4].Reason)
	require.Equal(t, "mtix agent allow worker", got[3].Fix, "the fix is the command only")
	require.Contains(t, got[3].Reason, "MTIX-112.40.10", "held names the follow-up that sends a fresh nudge on approval")
	require.Equal(t, got[5].Since, ago(10*time.Minute), "legacy rows without an outcome time fall back to the claim time")
}

func TestWakeStatusRecent_ErrorsAndBadTimes_AreVisible(t *testing.T) {
	f := okFixture()
	f.rowsErr = errors.New("db down")
	_, err := f.svc().Recent(t.Context(), "worker", 5)
	require.ErrorContains(t, err, "db down")

	f = okFixture()
	f.settingsErr = errors.New("bad settings")
	_, err = f.svc().Recent(t.Context(), "worker", 5)
	require.ErrorContains(t, err, "bad settings")

	f = okFixture()
	f.rows = []model.WakeStatusRow{{EventSeq: 1, Outcome: model.WakeSent, OutcomeAt: "garbage"}}
	got, err := f.svc().Recent(t.Context(), "worker", 5)
	require.NoError(t, err)
	require.Equal(t, model.WakeStateUnknown, got[0].State, "an unreadable time is unknown, never nudged")
	require.Contains(t, got[0].Reason, "time")
}

func TestWakeStatusRecent_UnrecognizedOutcome_NamesTheStoredValue(t *testing.T) {
	f := okFixture()
	f.rows = []model.WakeStatusRow{{EventSeq: 1, Outcome: model.WakeOutcome("weirdo"), ClaimedAt: statusNow.Format(time.RFC3339Nano)}}
	got, err := f.svc().Recent(t.Context(), "worker", 5)
	require.NoError(t, err)
	require.Equal(t, model.WakeStateUnknown, got[0].State)
	require.Equal(t, "unrecognized outcome weirdo", got[0].Reason)
}

func TestWakeStatusRecent_SourceErrors_AreTypedBySource(t *testing.T) {
	f := okFixture()
	f.settingsErr = errors.New("bad settings")
	_, err := f.svc().Recent(t.Context(), "worker", 5)
	var src *service.WakeSourceError
	require.ErrorAs(t, err, &src)
	require.Equal(t, "settings", src.Source)

	f = okFixture()
	f.rowsErr = errors.New("db down")
	_, err = f.svc().Recent(t.Context(), "worker", 5)
	require.ErrorAs(t, err, &src)
	require.Equal(t, "ledger", src.Source)
}

func TestWakeStatusUnreadable_ReturnsUnknownWithCause(t *testing.T) {
	got := okFixture().svc().Unreadable("worker", errors.New("wake.json is corrupt"))
	require.Equal(t, model.WakeStateUnknown, got.State)
	require.Contains(t, got.Reason, "wake.json is corrupt")
	require.NotEmpty(t, got.Fix)
}

func TestWakeStatusRecent_FutureLedgerTime_NeverShowsNegativeAge(t *testing.T) {
	f := okFixture()
	future := statusNow.Add(10 * time.Minute).Format(time.RFC3339Nano)
	f.rows = []model.WakeStatusRow{{EventSeq: 1, Outcome: model.WakeSent, Route: "hook-waiter", OutcomeAt: future}}
	got, err := f.svc().Recent(t.Context(), "worker", 5)
	require.NoError(t, err)
	require.Equal(t, model.WakeStateNudged, got[0].State, "a nudge dated in the future is still inside its window")
	require.Equal(t, int64(0), got[0].AgeSeconds, "clock skew must not produce a negative age")
	require.Equal(t, future, got[0].Since)
}
