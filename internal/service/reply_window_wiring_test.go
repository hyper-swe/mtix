// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestContactCadence_ReplyWindowInsideFastPhase_KeepsFiveSecondCadence(t *testing.T) {
	f := newReplyFixture(t, t0, t0) // fresh activity: the 5 s fast phase
	f.openWindow("peer", 2*time.Hour)
	f.run(time.Minute)
	require.GreaterOrEqual(t, f.dialsBetween(t0, t0.Add(time.Minute)), 11,
		"a reply window never slows the faster activity cadence down to 60 s")
	f.run(20 * time.Minute) // fast phase over: the window now sets the 60 s cadence
	require.Equal(t, 60*time.Second, f.dialTimes[len(f.dialTimes)-1].Sub(f.dialTimes[len(f.dialTimes)-2]))
}

func TestRecipientHold_SameHostSend_DoesNotHold(t *testing.T) {
	f := activeAt(t)
	f.j.regs["host-a"] = append(f.j.regs["host-a"], "sibling-agent")
	f.j.deliver("sibling-agent", "local-agent", t0.Add(2*time.Hour)) // both agents live on this host
	f.run(time.Minute)
	require.Empty(t, f.replyState().Holds, "a send between two agents of one host needs no hub contact")
	require.Empty(t, f.replyState().Extensions, "and is not counted as a remote extension")
	require.Positive(t, f.replyState().Cursor, "the event was still examined once")
}

// reentrantJournal runs a complete second scan while the first one is between
// reading the cursor and applying its events: the shape of two overlapping
// dispatcher processes.
type reentrantJournal struct {
	*fakeJournal
	other *service.ReplyProcessor
	t     *testing.T
	once  sync.Once
}

func (j *reentrantJournal) ReplyByEvents(ctx context.Context, after int64, limit int) ([]sqlite.ReplyByEvent, error) {
	got, err := j.fakeJournal.ReplyByEvents(ctx, after, limit)
	j.once.Do(func() { require.NoError(j.t, j.other.Scan(ctx)) })
	return got, err
}

func TestReplyScan_OverlappingScans_ApplyAnEventOnce(t *testing.T) {
	f := activeAt(t)
	f.j.deliver("stranger", "local-agent", t0.Add(2*time.Hour))
	rj := &reentrantJournal{fakeJournal: f.j, t: t}
	rj.other = &service.ReplyProcessor{Journal: f.j, State: f.state, Key: replyBoardKey, Host: f.proc.Host, Settings: f, Clock: f.proc.Clock}
	f.proc.Journal = rj
	require.NoError(t, f.proc.Scan(t.Context()))
	st := f.replyState()
	require.Len(t, st.Holds, 1, "the second scan already applied the event; the first must skip it")
	require.Len(t, st.Extensions["stranger"], 1, "one extension, not two")
}

func TestReplyScan_ConcurrentScans_ApplyEachEventOnce(t *testing.T) {
	f := activeAt(t)
	for i := 0; i < 5; i++ {
		f.j.deliver("stranger", "local-agent", t0.Add(2*time.Hour))
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &service.ReplyProcessor{Journal: f.j, State: f.state, Key: replyBoardKey, Host: f.proc.Host, Settings: f, Clock: f.proc.Clock}
			assertNoErr(t, p.Scan(t.Context()))
		}()
	}
	wg.Wait()
	st := f.replyState()
	require.Len(t, st.Holds, 5, "five events, five holds, however many scans overlapped")
	require.Len(t, st.Extensions["stranger"], 5)
	require.EqualValues(t, 5, st.Cursor)
}

func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Error(err)
	}
}

func TestOpenReplyWindow_ReplacesSameWindowAndBoundsTheList(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	win := func(awaiting string, at time.Time) oplocal.ReplyWindow {
		return oplocal.ReplyWindow{AwaitingFrom: awaiting, Sender: "me", Node: "N-1", OpenedAt: at, ReplyBy: at.Add(time.Hour)}
	}
	f.openWindow("peer", time.Hour)
	require.NoError(t, service.OpenReplyWindow(t.Context(), f.state, replyBoardKey, win("peer", t0.Add(time.Minute))))
	require.Len(t, f.replyState().Windows, 1, "a second window to the same agent and node replaces the first")
	for i := 0; i < 70; i++ {
		require.NoError(t, service.OpenReplyWindow(t.Context(), f.state, replyBoardKey, win("agent-"+time.Duration(i).String(), t0.Add(time.Minute))))
	}
	ws := f.replyState().Windows
	require.Len(t, ws, 64, "the list is bounded; the newest windows are kept")
	require.Equal(t, "agent-"+time.Duration(69).String(), ws[len(ws)-1].AwaitingFrom)
}

// Reply board dispatcher whose pull and journal the test controls.
func (f *hostFixture) replyBoardDispatcher(kind service.HubContactKind, journal service.ReplyJournal, pull func()) *service.HostDispatcher {
	f.t.Helper()
	host := service.RegistrationHost{HostID: f.host, UID: "test-uid"}
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) {
		return service.NewHostBoard(service.HostBoardOptions{Store: f.store, State: f.state, Path: f.root,
			Host: service.RegistrationHost{HostID: f.host, UID: "test-uid", Version: "test", Protocol: model.AttachProtocol},
			PID:  42, Clock: func() time.Time { return f.now }, Liveness: &scanLiveness{}, Adapters: []service.WakeAdapter{f.adapter},
			Contact: service.NewContactScheduler(func() time.Time { return f.now }, func() (service.ContactInputs, error) {
				return service.ContactInputs{Mode: service.HubContactMode{Kind: kind, Interval: time.Second}}, nil
			}),
			Replies: &service.ReplyProcessor{Journal: journal, State: f.state, Key: replyBoardKey, Host: host, Settings: f.state, Clock: func() time.Time { return f.now }},
			Pull:    func(context.Context, string) error { f.dials++; pull(); return nil }, Hooks: func(context.Context) { f.hooks++ }})
	}})
	require.NoError(f.t, err)
	return d
}

func (f *hostFixture) replyState() oplocal.ReplyState {
	st, err := f.state.ReadReplyState(replyBoardKey)
	require.NoError(f.t, err)
	return st
}

func TestHostBoard_Replies_HoldStartsInThePassThatPulled(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	j := &fakeJournal{answered: map[string]bool{}, regs: map[string][]string{f.host: {"local-agent"}}}
	deadline := f.now.Add(2 * time.Hour)
	d := f.replyBoardDispatcher(service.HubContactInterval, j, func() { j.deliver("remote-agent", "local-agent", deadline) })
	require.NoError(t, d.Pass(t.Context()))
	require.Equal(t, 1, f.dials)
	holds := f.replyState().Holds
	require.Len(t, holds, 1, "the scan runs after the pull, so the event it fetched holds in the same pass")
	require.True(t, holds[0].Until.Equal(deadline))
}

func TestHostBoard_Replies_TickDropsExpiredWindowInAPassWithoutPull(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	require.NoError(t, service.OpenReplyWindow(t.Context(), f.state, replyBoardKey, oplocal.ReplyWindow{
		AwaitingFrom: "peer", Sender: "me", Node: "N-1", OpenedAt: f.now.Add(-2 * time.Hour), ReplyBy: f.now.Add(-time.Hour)}))
	require.Len(t, f.replyState().Windows, 1)
	j := &fakeJournal{answered: map[string]bool{}}
	d := f.replyBoardDispatcher(service.HubContactManual, j, func() {})
	require.NoError(t, d.Pass(t.Context()))
	require.Zero(t, f.dials, "manual mode: no pull, so no scan")
	require.Empty(t, f.replyState().Windows, "every pass settles the accounting, even without a pull")
}
