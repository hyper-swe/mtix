// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// daemonReplyJournal is the local journal the daemon's reply scan reads.
type daemonReplyJournal struct {
	mu       sync.Mutex
	events   []sqlite.ReplyByEvent
	answered map[string]bool
	regs     []string // agents registered on the daemon's host
}

func (j *daemonReplyJournal) ReplyByEvents(_ context.Context, after int64, limit int) ([]sqlite.ReplyByEvent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []sqlite.ReplyByEvent
	for _, e := range j.events {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (j *daemonReplyJournal) ReplyArrived(_ context.Context, from, to string, _ int64) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.answered[from+">"+to], nil
}

func (j *daemonReplyJournal) ListRegistrations(context.Context, string, string) ([]sqlite.Registration, error) {
	var out []sqlite.Registration
	for _, a := range j.regs {
		out = append(out, sqlite.Registration{AgentID: a})
	}
	return out, nil
}

// daemonReplyRig drives the daemon's real pass path (runScheduledPass with the
// plan's reply processor, daemonPassFor with the post-pull scan) on a fake clock.
type daemonReplyRig struct {
	t       *testing.T
	now     time.Time
	src     *hubContactSource
	journal *daemonReplyJournal
	replies *service.ReplyProcessor
	plan    daemonLoopPlan
	pulls   []time.Time
	onPull  func()
}

const daemonRigActivityAge = time.Hour // idle: only a reply window or hold can cause contact

func newDaemonReplyRig(t *testing.T, activityAge time.Duration) *daemonReplyRig {
	t.Helper()
	src := hubSourceFixture(t, "prefix: \"X\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(src.mtixDir, "config.yaml"),
		[]byte("prefix: \"X\"\nsync:\n  relay:\n    poll_interval: 0\n"), 0o600))
	r := &daemonReplyRig{t: t, now: schedT0, src: src,
		journal: &daemonReplyJournal{answered: map[string]bool{}, regs: []string{"local-agent"}}}
	src.now = func() time.Time { return r.now }
	saved := app
	app = appContext{mtixDir: src.mtixDir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() { app = saved })
	r.replies = newReplyProcessor(r.journal, src.state, src, service.RegistrationHost{HostID: "host-a", UID: "u"})
	activity := schedT0.Add(-activityAge)
	sched := service.NewContactScheduler(src.now, func() (service.ContactInputs, error) {
		return service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactActivity},
			LastActivity: activity, Reply: src.replyInputs()}, nil
	})
	pass := daemonPassFor(t.Context(), io.Discard, true, r.replies, func() {
		r.pulls = append(r.pulls, r.now)
		if r.onPull != nil {
			r.onPull()
		}
	})
	r.plan = daemonLoopPlan{sched: sched, replies: r.replies, pass: pass}
	return r
}

func (r *daemonReplyRig) run(d time.Duration) {
	end := r.now.Add(d)
	for r.now.Before(end) {
		runScheduledPass(r.t.Context(), os.Stderr, r.plan, daemonPassControl{})
		r.now = r.now.Add(5 * time.Second)
	}
}

func (r *daemonReplyRig) pullsBetween(from, to time.Time) int {
	n := 0
	for _, p := range r.pulls {
		if !p.Before(from) && p.Before(to) {
			n++
		}
	}
	return n
}

func (r *daemonReplyRig) state() oplocal.ReplyState {
	st, err := r.src.state.ReadReplyState(r.src.key())
	require.NoError(r.t, err)
	return st
}

func TestDaemonReplyWindow_ReplyArrives_ClosesWindowAndPullsDropToNormal(t *testing.T) {
	r := newDaemonReplyRig(t, daemonRigActivityAge)
	require.NoError(t, service.OpenReplyWindow(t.Context(), r.src.state, r.src.key(),
		oplocal.ReplyWindow{AwaitingFrom: "peer", Sender: "me", Node: "N-1", OpenedAt: r.now, ReplyBy: r.now.Add(time.Hour)}))

	r.run(10 * time.Minute)
	open := r.pullsBetween(schedT0, schedT0.Add(10*time.Minute))
	require.InDelta(t, 10, open, 1, "an open window pulls every 60 s on an otherwise idle board")
	require.Len(t, r.state().Windows, 1)

	r.journal.answered["peer>me"] = true // the awaited reply is now in the journal
	r.run(2 * time.Minute)               // the next pull's scan sees it
	require.Empty(t, r.state().Windows, "the daemon closes the window on the addressed reply, not at the TTL")

	closedAt := r.now
	r.run(20 * time.Minute)
	require.Zero(t, r.pullsBetween(closedAt, r.now), "after the reply the cadence is back to normal: idle means no pulls")
}

func TestDaemonRecipientHold_EventForLocalAgent_HoldsUntilItsDeadline(t *testing.T) {
	r := newDaemonReplyRig(t, 0) // fresh activity: the first pass pulls
	deadline := schedT0.Add(2 * time.Hour)
	delivered := false
	r.onPull = func() { // the pull fetches a remote sender's event for a local agent
		if !delivered {
			delivered = true
			r.journal.events = append(r.journal.events, sqlite.ReplyByEvent{Seq: 1, EventID: "ev1",
				Author: "remote-agent", To: "local-agent", ReplyBy: model.FormatReplyBy(deadline)})
		}
	}
	r.run(5 * time.Second)
	hold := r.state().Holds
	require.Len(t, hold, 1, "the same pass that pulled the event starts the hold")
	require.True(t, hold[0].Until.Equal(deadline), "the hold ends at the (clamped) deadline")

	r.run(deadline.Add(-time.Minute).Sub(r.now)) // run until just before the deadline
	require.Positive(t, r.pullsBetween(schedT0.Add(time.Hour), deadline), "still pulling every 60 s long after the activity windows ended")
	r.run(3 * time.Minute) // past the deadline
	after := r.now
	r.run(30 * time.Minute)
	require.Zero(t, r.pullsBetween(after, r.now), "the hold ended at its deadline: no pulls on the idle board")
	require.Empty(t, r.state().Holds)
}
