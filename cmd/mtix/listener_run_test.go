// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// listenerBuffer is a mutex-guarded buffer the loop goroutine and the test share.
// It is local to this file so the package's tests build on every platform.
type listenerBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *listenerBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *listenerBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// listenerWorld is a fake board: a clock, the scheduler inputs, a hub dial
// counter and a pass loop driven tick by tick.
type listenerWorld struct {
	mu      sync.Mutex // guards now, in and dials: the loop goroutine reads them
	now     time.Time
	in      service.ContactInputs
	dials   int
	ticks   chan time.Time
	passed  chan struct{} // one value per finished pass
	stopped chan struct{} // closed when the loop returned
	err     error
	out     listenerBuffer
}

func (w *listenerWorld) setLease(live bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.in.LiveLease = live
}

func (w *listenerWorld) dialCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dials
}

func startListenerWorld(t *testing.T, in service.ContactInputs, hub, relay bool) *listenerWorld {
	t.Helper()
	schedFixture(t)
	w := &listenerWorld{now: schedT0, in: in, ticks: make(chan time.Time), passed: make(chan struct{}, 64), stopped: make(chan struct{})}
	load := func() (service.ContactInputs, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.in, nil
	}
	clock := func() time.Time {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.now
	}
	l := &daemonListener{load: load, now: clock, hub: hub, relay: relay}
	plan := daemonLoopPlan{
		label: "mtix daemon", interval: 5,
		// passed is signalled after the exit check, so a test that changes the inputs
		// after a tick can never race the check of the pass it just waited for.
		done: func() (bool, string) {
			done, why := l.Done()
			w.passed <- struct{}{}
			return done, why
		},
		pass: func(contact bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if contact && hub {
				w.dials++
			}
		},
		controls: []daemonPassControl{{ticks: w.ticks}},
	}
	if hub {
		plan.sched = service.NewContactScheduler(clock, load)
		plan.onContact = func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		w.err = runDaemonPassLoop(ctx, &w.out, io.Discard, plan)
		close(w.stopped)
	}()
	<-w.passed // the arm-time pass
	return w
}

// tick advances the clock by d and delivers one tick; it reports whether the
// loop took it (false: the loop had already exited).
func (w *listenerWorld) tick(d time.Duration) bool {
	w.mu.Lock()
	w.now = w.now.Add(d)
	at := w.now
	w.mu.Unlock()
	select {
	case w.ticks <- at:
		<-w.passed
		return true
	case <-w.stopped:
		return false
	}
}

func (w *listenerWorld) exited(t *testing.T) {
	t.Helper()
	select {
	case <-w.stopped:
		require.NoError(t, w.err)
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not exit")
	}
}

func holdInputs(lease bool) service.ContactInputs {
	return service.ContactInputs{
		Mode:         service.HubContactMode{Kind: service.HubContactActivity},
		Listen:       service.ListenSettings{Hold: true, Interval: 10 * time.Second, MaxIdle: 8 * time.Hour},
		LastActivity: schedT0, LiveLease: lease,
	}
}

func TestListener_LastLeaseGone_ExitsAndStopsDialing(t *testing.T) {
	w := startListenerWorld(t, holdInputs(true), true, false)
	for i := 0; i < 4; i++ { // a few passes inside the hold, ending past the 30 minute activity window
		require.True(t, w.tick(10*time.Minute))
	}
	require.Greater(t, w.dialCount(), 1, "while a lease is live the hold keeps dialing past the activity window")

	w.setLease(false) // the last waiter ended
	require.True(t, w.tick(5*time.Second), "the pass after the lease goes still runs")
	w.exited(t)
	require.Contains(t, w.out.String(), "no live waiter lease is left; exiting")

	dialsAtExit := w.dialCount()
	require.False(t, w.tick(time.Hour), "a simulated hour later nothing is running to take a tick")
	require.Equal(t, dialsAtExit, w.dialCount(), "zero hub dials after exit")
}

func TestListener_ArmTime_PullsOnce(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*service.ContactInputs)
		hub       bool
		wantDials int
	}{
		{"hold armed: pull at arm, none inside the interval", func(*service.ContactInputs) {}, true, 1},
		{"no recorded activity: idle-clamped, no pull until real activity", func(in *service.ContactInputs) { in.LastActivity = time.Time{} }, true, 0},
		{"idle clamp passed: no pull", func(in *service.ContactInputs) { in.LastActivity = schedT0.Add(-9 * time.Hour) }, true, 0},
		{"manual never dials", func(in *service.ContactInputs) { in.Mode = service.HubContactMode{Kind: service.HubContactManual} }, true, 0},
		{"listen off adds no arm pull (activity window closed)", func(in *service.ContactInputs) { in.Listen.Hold = false; in.LastActivity = time.Time{} }, true, 0},
		{"no hub never dials", func(*service.ContactInputs) {}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := holdInputs(true)
			tt.mutate(&in)
			w := startListenerWorld(t, in, tt.hub, !tt.hub)
			// The first pass ran before any tick. Give it a moment inside the interval.
			if w.tick(time.Second) {
				require.Equal(t, tt.wantDials, w.dialCount(), "one arm-time pull, none a second later")
			}
			w.setLease(false)
			w.tick(time.Second)
			w.exited(t)
			require.Equal(t, tt.wantDials, w.dialCount())
		})
	}
}

func TestListener_ArmTime_ThenFollowsListenInterval(t *testing.T) {
	w := startListenerWorld(t, holdInputs(true), true, false)
	require.True(t, w.tick(2*time.Second))
	require.Equal(t, 1, w.dialCount())
	require.True(t, w.tick(10*time.Second))
	require.Equal(t, 2, w.dialCount(), "the next contact is one listen interval after the arm pull")
	w.setLease(false)
	w.tick(time.Second)
	w.exited(t)
}

func TestListener_IdleClamp_EndsHubListener(t *testing.T) {
	w := startListenerWorld(t, holdInputs(true), true, false)
	require.True(t, w.tick(time.Hour))
	require.True(t, w.tick(8*time.Hour), "past the idle clamp the hold has ended")
	w.exited(t)
	require.Contains(t, w.out.String(), "the listen hold has ended")
}

func TestListener_RelayBoard_IngestsDuringHold(t *testing.T) {
	relayPasses, pulls := 0, 0
	pass := buildDaemonPass(false, daemonPassSteps{
		drain: func() {}, pull: func() { pulls++ }, relay: func() { relayPasses++ }, dispatch: func() {},
	})
	w := startListenerWorld(t, holdInputs(true), false, true)
	require.True(t, w.tick(time.Hour))
	require.True(t, w.tick(10*time.Hour), "a relay board stays up while a waiter lives, past any hub clamp")
	require.True(t, w.tick(time.Hour), "the loop is still running after the pass past the clamp: it takes another tick")
	select {
	case <-w.stopped:
		t.Fatal("a relay board's listener must not exit at the hub idle clamp while a waiter lives")
	default:
	}
	pass(false)
	pass(true)
	require.Equal(t, 2, relayPasses, "the relay phase runs on every pass during the hold")
	require.Zero(t, pulls, "a relay board makes zero hub dials")
	require.Zero(t, w.dialCount())

	w.setLease(false)
	w.tick(time.Second)
	w.exited(t)
}

func TestDaemonListener_UnreadableInputs_ExitFailSafe(t *testing.T) {
	l := &daemonListener{load: func() (service.ContactInputs, error) { return service.ContactInputs{}, service.ErrContactInputs },
		now: func() time.Time { return schedT0 }, hub: true}
	done, why := l.Done()
	require.True(t, done)
	require.Contains(t, why, "unreadable")
}

func TestListener_Repark_PastClamp_ZeroDials_ThenActivityCausesOnePull(t *testing.T) {
	now := schedT0.Add(9 * time.Hour) // past the 8h idle clamp
	in := holdInputs(true)
	dials := 0
	// A waiter re-parks every 300 s for a simulated 24 h; each park starts a
	// fresh listener process (a fresh scheduler, nothing remembered).
	for at := now; at.Before(now.Add(24 * time.Hour)); at = at.Add(300 * time.Second) {
		clock := at
		sched := service.NewContactScheduler(func() time.Time { return clock },
			func() (service.ContactInputs, error) { return in, nil })
		l := &daemonListener{load: func() (service.ContactInputs, error) { return in, nil },
			now: func() time.Time { return clock }, hub: true}
		if sched.Decide().Contact {
			dials++
		}
		done, _ := l.Done()
		require.True(t, done, "past the clamp the new listener exits after its first pass")
	}
	require.Zero(t, dials, "re-parking a wait is not activity: zero hub dials for 24 h past the clamp")

	// Real activity (an addressed event or a local write) re-arms the hold.
	in.LastActivity = now.Add(25 * time.Hour)
	clock := in.LastActivity.Add(time.Minute)
	sched := service.NewContactScheduler(func() time.Time { return clock },
		func() (service.ContactInputs, error) { return in, nil })
	require.True(t, sched.Decide().Contact, "activity then causes the one pull")
	sched.MarkContact()
	require.False(t, sched.Decide().Contact, "and only one inside the listen interval")
}

func TestListener_ClampedBoard_ListenerDialsNothingThroughTheLoop(t *testing.T) {
	in := holdInputs(true)
	in.LastActivity = schedT0.Add(-9 * time.Hour)
	w := startListenerWorld(t, in, true, false)
	w.exited(t)
	require.Zero(t, w.dialCount(), "a listener spawned past the clamp makes no hub contact")
}
