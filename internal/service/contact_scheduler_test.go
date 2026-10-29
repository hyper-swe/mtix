// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

var t0 = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

type fakeBoard struct {
	now    time.Time
	in     service.ContactInputs
	dials  int
	sched  *service.ContactScheduler
	local  time.Duration
	hasErr error
}

func newFakeBoard(in service.ContactInputs) *fakeBoard {
	b := &fakeBoard{now: t0, in: in, local: 5 * time.Second}
	b.sched = service.NewContactScheduler(func() time.Time { return b.now },
		func() (service.ContactInputs, error) { return b.in, b.hasErr })
	return b
}

// step runs one daemon pass and advances the fake clock by the local tick.
func (b *fakeBoard) step() service.ContactDecision {
	d := b.sched.Decide()
	if d.Contact {
		b.dials++
		b.sched.MarkContact()
	}
	b.now = b.now.Add(b.local)
	return d
}

func (b *fakeBoard) run(d time.Duration) {
	end := b.now.Add(d)
	for b.now.Before(end) {
		b.step()
	}
}

func activityInputs() service.ContactInputs {
	return service.ContactInputs{
		Mode:         service.HubContactMode{Kind: service.HubContactActivity},
		Listen:       service.ListenSettings{Hold: false, Interval: 10 * time.Second, MaxIdle: 8 * time.Hour},
		LastActivity: t0,
	}
}

func TestHubContactActivity_ThirtyMinutesAfterActivity_ZeroDials(t *testing.T) {
	b := newFakeBoard(activityInputs())
	b.run(30 * time.Minute)
	dialsInWindow := b.dials
	require.Positive(t, dialsInWindow, "the window itself contacts the hub")
	// Fast phase: 5s for 10 min = 120; slow phase: 60s for 20 min = 20.
	require.InDelta(t, 140, dialsInWindow, 3)

	b.run(time.Hour) // a simulated idle hour after the window
	require.Equal(t, dialsInWindow, b.dials, "zero dials after the window closes")
}

func TestHubContactActivity_FastThenSlowPhases(t *testing.T) {
	b := newFakeBoard(activityInputs())
	b.run(10 * time.Minute)
	fast := b.dials
	require.InDelta(t, 120, fast, 2)
	b.run(10 * time.Minute)
	require.InDelta(t, 10, b.dials-fast, 2, "60s cadence after the fast phase")
}

func TestHubContactActivity_NoActivityRecorded_NeverDials(t *testing.T) {
	in := activityInputs()
	in.LastActivity = time.Time{}
	b := newFakeBoard(in)
	b.run(time.Hour)
	require.Zero(t, b.dials)
}

func TestHubContactActivity_ParkedWait_DoesNotExtend(t *testing.T) {
	in := activityInputs()
	in.LiveLease = true // a parked wait holds a lease and heartbeats; hold is off
	b := newFakeBoard(in)
	b.run(30 * time.Minute)
	after := b.dials
	b.run(time.Hour)
	require.Equal(t, after, b.dials, "a live registration never extends the window")
}

func TestHubContactInterval_ContactsOnSchedule(t *testing.T) {
	in := service.ContactInputs{Mode: service.HubContactMode{
		Kind: service.HubContactInterval, Interval: 30 * time.Second}}
	b := newFakeBoard(in)
	b.run(time.Hour)
	require.InDelta(t, 120, b.dials, 1)
}

func TestHubContactManual_NeverDials(t *testing.T) {
	in := activityInputs()
	in.Mode = service.HubContactMode{Kind: service.HubContactManual}
	in.Listen.Hold = true
	in.LiveLease = true
	b := newFakeBoard(in)
	b.run(2 * time.Hour)
	require.Zero(t, b.dials, "manual never contacts, even with a live listener")
}

func TestContactScheduler_LoadError_NeverDials(t *testing.T) {
	b := newFakeBoard(activityInputs())
	b.hasErr = service.ErrContactInputs
	b.run(time.Hour)
	require.Zero(t, b.dials, "unreadable inputs fail safe: no contact")
}

func listenInputs() service.ContactInputs {
	in := activityInputs()
	in.Listen.Hold = true
	in.LiveLease = true
	return in
}

func TestContactScheduler_ListenHold_PullsAtListenInterval(t *testing.T) {
	b := newFakeBoard(listenInputs())
	b.run(35 * time.Minute) // past the activity window: only the hold keeps contacting
	before := b.dials
	b.run(time.Hour)
	require.InDelta(t, 360, b.dials-before, 4, "3600s / 10s listen interval")
	d := b.sched.Decide()
	require.Equal(t, "listen-hold", d.Reason)
	require.Equal(t, t0.Add(8*time.Hour), d.HoldDeadline)
}

func TestContactScheduler_NoLiveLease_HoldEnds(t *testing.T) {
	b := newFakeBoard(listenInputs())
	b.run(time.Hour)
	b.in.LiveLease = false
	before := b.dials
	b.run(time.Hour)
	require.Equal(t, before, b.dials)
}

func TestContactScheduler_MaxIdleClamp_EndsHold(t *testing.T) {
	in := listenInputs()
	in.Listen.MaxIdle = 2 * time.Hour
	b := newFakeBoard(in)
	b.run(2 * time.Hour)
	at := b.dials
	b.run(time.Hour)
	require.Equal(t, at, b.dials, "no contact past the idle clamp")
}

func TestContactScheduler_ListenOff_StrictD18(t *testing.T) {
	in := listenInputs()
	in.Listen.Hold = false
	b := newFakeBoard(in)
	b.run(30 * time.Minute)
	at := b.dials
	b.run(time.Hour)
	require.Equal(t, at, b.dials)
}

func TestContactScheduler_ParkedWait_NotActivity(t *testing.T) {
	in := listenInputs()
	b := newFakeBoard(in)
	b.run(9 * time.Hour) // lease heartbeats do not move LastActivity
	require.Equal(t, t0, b.in.LastActivity)
	at := b.dials
	b.run(time.Hour)
	require.Equal(t, at, b.dials, "hold ended 8h after the last real activity")
}

func TestContactScheduler_FreshActivity_RestartsClamp(t *testing.T) {
	b := newFakeBoard(listenInputs())
	b.run(7 * time.Hour)
	b.in.LastActivity = b.now // a woken turn is activity
	b.run(7 * time.Hour)
	d := b.sched.Decide()
	require.Equal(t, "listen-hold", d.Reason)
}

func TestContactScheduler_NoActivity_NoHoldEvenWithLease(t *testing.T) {
	in := listenInputs()
	in.LastActivity = time.Time{} // arming records activity; none recorded means no hold
	b := newFakeBoard(in)
	b.run(time.Hour)
	require.Zero(t, b.dials)
}

func TestContactScheduler_FutureDatedActivity_IsStale(t *testing.T) {
	in := listenInputs()
	in.LastActivity = t0.Add(48 * time.Hour) // clock stepped back or hand edit
	b := newFakeBoard(in)
	b.run(time.Hour)
	require.Zero(t, b.dials, "no fast phase and no hold from a future-dated record")

	in.LastActivity = t0.Add(2 * time.Minute) // within the skew allowance
	b = newFakeBoard(in)
	b.run(time.Minute)
	require.Positive(t, b.dials)
}

func TestContactScheduler_FastPhasePlusHold_UsesShorterCadence(t *testing.T) {
	in := listenInputs()
	in.Listen.Interval = 2 * time.Second // shorter than the 5s fast phase
	b := newFakeBoard(in)
	b.local = time.Second
	b.run(10 * time.Minute)
	require.InDelta(t, 300, b.dials, 3, "hold interval wins when shorter than the fast phase")

	in.Listen.Interval = 30 * time.Second // longer: the fast phase wins
	b = newFakeBoard(in)
	b.run(10 * time.Minute)
	require.InDelta(t, 120, b.dials, 3)
}

func TestContactScheduler_Delay_NeverExceedsLocalTick(t *testing.T) {
	b := newFakeBoard(activityInputs())
	require.LessOrEqual(t, b.sched.Delay(5*time.Second), 5*time.Second)
	in := activityInputs()
	in.Mode = service.HubContactMode{Kind: service.HubContactManual}
	b2 := newFakeBoard(in)
	require.Equal(t, 5*time.Second, b2.sched.Delay(5*time.Second), "local work keeps its tick")
}

func TestListenHoldDefault_IsOneConstant(t *testing.T) {
	got, err := service.ParseListen("")
	require.NoError(t, err)
	require.Equal(t, service.DefaultListenHold, got)
	want := "off"
	if service.DefaultListenHold {
		want = "live"
	}
	require.Equal(t, want, service.DefaultListenValue())
}

func TestContactScheduler_Delay_ShortensLongLocalTickToDueTime(t *testing.T) {
	b := newFakeBoard(activityInputs())
	b.sched.MarkContact()
	b.now = b.now.Add(2 * time.Second)
	require.Equal(t, 3*time.Second, b.sched.Delay(time.Minute),
		"fast phase is 5s: 2s after a contact the next pass is due in 3s, not at the 60s local tick")
}
