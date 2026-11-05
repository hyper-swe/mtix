// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// strictJournal behaves like the real journal: a blank agent id is an
// ErrInvalidInput, and failErr (when set) is a real I/O failure.
type strictJournal struct {
	*fakeJournal
	failErr error
	// invalidFor makes the journal reject this awaited agent as invalid input.
	invalidFor string
}

func (j *strictJournal) ReplyArrived(ctx context.Context, from, to string, seq int64) (bool, error) {
	if j.failErr != nil {
		return false, j.failErr
	}
	if from == "" || to == "" || (j.invalidFor != "" && from == j.invalidFor) {
		return false, fmt.Errorf("reply arrived: both agents required: %w", model.ErrInvalidInput)
	}
	return j.fakeJournal.ReplyArrived(ctx, from, to, seq)
}

// writeRawWindows puts windows on disk without OpenReplyWindow's checks, as
// an older binary would have.
func (f *replyFixture) writeRawWindows(ws ...oplocal.ReplyWindow) {
	f.t.Helper()
	require.NoError(f.t, f.state.UpdateReplyState(f.t.Context(), replyBoardKey, func(st *oplocal.ReplyState) error {
		st.Windows = append(st.Windows, ws...)
		return nil
	}))
}

func (f *replyFixture) window(awaiting, sender, node string) oplocal.ReplyWindow {
	return oplocal.ReplyWindow{AwaitingFrom: awaiting, Sender: sender, Node: node, OpenedAt: f.now, ReplyBy: f.now.Add(time.Hour)}
}

func TestOpenReplyWindow_BlankSenderOrAwaitingFrom_ReturnsInvalidInput(t *testing.T) {
	for name, tc := range map[string][2]string{
		"blank sender":        {"peer", ""},
		"blank awaiting":      {"", "me"},
		"whitespace sender":   {"peer", "  \t"},
		"whitespace awaiting": {" ", "me"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReplyFixture(t, t0, t0)
			w := f.window(tc[0], tc[1], "N-1")
			err := service.OpenReplyWindow(t.Context(), f.state, replyBoardKey, w)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.Empty(t, f.replyState().Windows, "nothing is stored")
		})
	}
}

func TestScan_SenderlessWindow_IsSkippedAndOthersStillClose(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	f.proc.Journal = &strictJournal{fakeJournal: f.j}
	f.writeRawWindows(f.window("peer", "", "N-1"), f.window("peer", "me", "N-2"), f.window("", "me", "N-3"))
	f.j.answered["peer>me"] = true
	f.j.regs["host-a"] = []string{"local-agent"}
	f.j.deliver("far", "local-agent", t0.Add(time.Hour))

	require.NoError(t, f.proc.Scan(t.Context()), "malformed data must not fail the pass")
	st := f.replyState()
	require.Empty(t, st.Windows, "the answered window closed and the malformed ones were removed")
	require.Len(t, st.Skipped, 2)
	for _, s := range st.Skipped {
		require.NotEmpty(t, s.Reason)
		require.False(t, s.At.IsZero())
	}
	require.Len(t, st.Holds, 1, "the pulled event still turned into a hold")
}

func TestScan_SkippedRecord_IsBoundedAtSixteenNewestKept(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	f.proc.Journal = &strictJournal{fakeJournal: f.j}
	var ws []oplocal.ReplyWindow
	for i := 0; i < 20; i++ {
		ws = append(ws, f.window("peer", "", fmt.Sprintf("N-%d", i)))
	}
	f.writeRawWindows(ws...)
	require.NoError(t, f.proc.Scan(t.Context()))
	st := f.replyState()
	require.Len(t, st.Skipped, 16)
	require.Equal(t, "N-19", st.Skipped[15].Window.Node, "the newest are kept")
	require.Equal(t, "N-4", st.Skipped[0].Window.Node)
}

func TestScan_SkippedRecord_ClearsAfterAWhile(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	f.proc.Journal = &strictJournal{fakeJournal: f.j}
	f.writeRawWindows(f.window("peer", "", "N-1"))
	require.NoError(t, f.proc.Scan(t.Context()))
	require.Len(t, f.replyState().Skipped, 1)
	f.now = f.now.Add(25 * time.Hour)
	require.NoError(t, f.proc.Scan(t.Context()))
	require.Empty(t, f.replyState().Skipped, "entries age out after a day")
}

func TestScan_JournalIOError_StillFailsThePass(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	boom := errors.New("disk gone")
	f.proc.Journal = &strictJournal{fakeJournal: f.j, failErr: boom}
	f.writeRawWindows(f.window("peer", "me", "N-1"))
	require.ErrorIs(t, f.proc.Scan(t.Context()), boom)
	require.Len(t, f.replyState().Windows, 1, "a real error leaves the window alone")
	require.Empty(t, f.replyState().Skipped)
}

func TestScan_BlankWindow_SkippedEvenWhenJournalAcceptsBlank(t *testing.T) {
	f := newReplyFixture(t, t0, t0) // the fake journal does not reject blanks
	f.j.answered[">me"] = true
	f.writeRawWindows(f.window("", "me", "N-1"))
	require.NoError(t, f.proc.Scan(t.Context()))
	require.Len(t, f.replyState().Skipped, 1, "the scan checks blanks itself")
}

func TestReplyStatusAt_SkippedWindows_Counted(t *testing.T) {
	st := oplocal.ReplyState{Skipped: []oplocal.SkippedReplyWindow{{Reason: "r", At: t0}, {Reason: "r", At: t0}}}
	got := service.ReplyStatusAt(st, service.ReplyPolicy{}, t0)
	require.Equal(t, 2, got.SkippedWindows)
}

func TestScan_JournalRejectsWindowAsInvalidInput_SkippedNotFatal(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	f.proc.Journal = &strictJournal{fakeJournal: f.j, invalidFor: "weird"}
	f.writeRawWindows(f.window("weird", "me", "N-1"), f.window("peer", "me", "N-2"))
	f.j.answered["peer>me"] = true
	require.NoError(t, f.proc.Scan(t.Context()))
	st := f.replyState()
	require.Empty(t, st.Windows)
	require.Len(t, st.Skipped, 1)
	require.Equal(t, "N-1", st.Skipped[0].Window.Node)
}
