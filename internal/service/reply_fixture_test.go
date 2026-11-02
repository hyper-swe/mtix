// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const replyBoardKey = "0123456789abcdef"

// fakeJournal is the local journal the reply scan reads.
type fakeJournal struct {
	events   []sqlite.ReplyByEvent
	answered map[string]bool
	regs     map[string][]string // host id -> agents registered there
	regCalls int
}

func (j *fakeJournal) ReplyByEvents(_ context.Context, after int64, limit int) ([]sqlite.ReplyByEvent, error) {
	var out []sqlite.ReplyByEvent
	for _, e := range j.events {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (j *fakeJournal) ReplyArrived(_ context.Context, from, to string, _ int64) (bool, error) {
	return j.answered[from+">"+to], nil
}

func (j *fakeJournal) ListRegistrations(_ context.Context, hostID, _ string) ([]sqlite.Registration, error) {
	j.regCalls++
	var out []sqlite.Registration
	for _, a := range j.regs[hostID] {
		out = append(out, sqlite.Registration{AgentID: a, HostID: hostID})
	}
	return out, nil
}

func (j *fakeJournal) deliver(author, to string, replyBy time.Time) {
	seq := int64(len(j.events) + 1)
	j.events = append(j.events, sqlite.ReplyByEvent{Seq: seq, EventID: "ev" + time.Duration(seq).String(),
		Author: author, To: to, ReplyBy: model.FormatReplyBy(replyBy)})
}

// replyFixture drives the real scheduler and processor with a fake clock and a
// dial counter: a dial is a pass the scheduler allowed to contact the hub.
type replyFixture struct {
	t         *testing.T
	now       time.Time
	activity  time.Time
	state     *oplocal.State
	j         *fakeJournal
	vals      map[string]string
	proc      *service.ReplyProcessor
	sched     *service.ContactScheduler
	dials     int
	dialTimes []time.Time
}

func newReplyFixture(t *testing.T, start, lastActivity time.Time) *replyFixture {
	t.Helper()
	f := &replyFixture{t: t, now: start, activity: lastActivity, state: operatorAlertsFixture(t),
		j:    &fakeJournal{answered: map[string]bool{}, regs: map[string][]string{"host-a": {"local-agent"}, "host-b": {"far-agent"}}},
		vals: map[string]string{}}
	f.proc = &service.ReplyProcessor{Journal: f.j, State: f.state, Key: replyBoardKey, Host: service.RegistrationHost{HostID: "host-a", UID: "u"},
		Settings: f, Clock: func() time.Time { return f.now }}
	f.sched = service.NewContactScheduler(func() time.Time { return f.now }, f.inputs)
	return f
}

// WakeSettings serves the two optional limits from the fixture's map.
func (f *replyFixture) WakeSettings(string) (map[string]model.WakeSetting, []oplocal.Report, error) {
	out := map[string]model.WakeSetting{}
	for k, v := range f.vals {
		out[k] = model.WakeSetting{Value: v}
	}
	return out, nil, nil
}

func (f *replyFixture) inputs() (service.ContactInputs, error) {
	st, err := f.state.ReadReplyState(replyBoardKey)
	if err != nil {
		return service.ContactInputs{}, err
	}
	pol, err := service.ReadReplyPolicy(f)
	if err != nil {
		return service.ContactInputs{}, err
	}
	return service.ContactInputs{
		Mode:         service.HubContactMode{Kind: service.HubContactActivity},
		Listen:       service.ListenSettings{Interval: 10 * time.Second, MaxIdle: 8 * time.Hour},
		LastActivity: f.activity,
		Reply:        service.ReplyInputsFor(st, pol, f.now),
	}, nil
}

// pass is one host pass: settle accounting, ask the scheduler, and on a dial
// run the post-pull scan. The fake clock then advances five seconds.
func (f *replyFixture) pass() {
	require.NoError(f.t, f.proc.Tick(f.t.Context()))
	if f.sched.Decide().Contact {
		f.dials++
		f.dialTimes = append(f.dialTimes, f.now)
		f.sched.MarkContact()
		require.NoError(f.t, f.proc.Scan(f.t.Context()))
	}
	f.now = f.now.Add(5 * time.Second)
}

func (f *replyFixture) run(d time.Duration) {
	end := f.now.Add(d)
	for f.now.Before(end) {
		f.pass()
	}
}

func (f *replyFixture) openWindow(awaiting string, ttl time.Duration) {
	w := oplocal.ReplyWindow{AwaitingFrom: awaiting, Sender: "me", Node: "N-1", OpenedAt: f.now, ReplyBy: f.now.Add(ttl)}
	require.NoError(f.t, service.OpenReplyWindow(f.t.Context(), f.state, replyBoardKey, w))
}

func (f *replyFixture) replyState() oplocal.ReplyState {
	st, err := f.state.ReadReplyState(replyBoardKey)
	require.NoError(f.t, err)
	return st
}

// dialsBetween counts dials with from <= time < to.
func (f *replyFixture) dialsBetween(from, to time.Time) int {
	n := 0
	for _, d := range f.dialTimes {
		if !d.Before(from) && d.Before(to) {
			n++
		}
	}
	return n
}
