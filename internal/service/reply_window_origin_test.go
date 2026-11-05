// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// The hold decision follows where an event came from (pulled from the hub or
// written on this machine), not whether its author is registered here
// (MTIX-112.20.3, same-host rule).
func TestRecipientHold_EventOrigin_DecidesHold(t *testing.T) {
	tests := []struct {
		name     string
		deliver  func(f *replyFixture)
		wantHold bool
	}{
		{"unregistered local author does not hold", func(f *replyFixture) {
			f.j.deliverLocal("operator", "local-agent", t0.Add(2*time.Hour))
		}, false},
		{"registered local author does not hold", func(f *replyFixture) {
			f.j.regs["host-a"] = append(f.j.regs["host-a"], "sibling-agent")
			f.j.deliverLocal("sibling-agent", "local-agent", t0.Add(2*time.Hour))
		}, false},
		{"pulled event from a remote agent whose id equals a local id holds", func(f *replyFixture) {
			f.j.regs["host-a"] = append(f.j.regs["host-a"], "sibling-agent")
			f.j.deliver("sibling-agent", "local-agent", t0.Add(2*time.Hour))
		}, true},
		{"pulled event from an unknown remote agent holds", func(f *replyFixture) {
			f.j.deliver("remote-agent", "local-agent", t0.Add(2*time.Hour))
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := activeAt(t)
			tt.deliver(f)
			f.run(time.Minute)
			st := f.replyState()
			require.Positive(t, st.Cursor, "the event was examined once")
			if tt.wantHold {
				require.Len(t, st.Holds, 1)
				return
			}
			require.Empty(t, st.Holds)
			require.Empty(t, st.Extensions, "a local send is not counted as a remote extension")
		})
	}
}

func TestReplyExtensions_RefusedExtension_LeavesHistoryUnchanged(t *testing.T) {
	f := activeAt(t)
	f.vals[service.ReplyExtensionsKey] = "1"
	f.extend("chatty", 10*time.Minute)
	f.extend("chatty", 10*time.Minute) // refused: past the limit of one
	require.Len(t, f.replyState().Extensions["chatty"], 1, "a refused extension is not recorded")
}

func TestContactDecision_ReplyReason_NamesWindowOrHold(t *testing.T) {
	win := newReplyFixture(t, t0.Add(3*time.Hour), t0)
	win.openWindow("peer", time.Hour)
	require.Equal(t, "reply-window", win.sched.Decide().Reason)

	hold := newReplyFixture(t, t0.Add(3*time.Hour), t0)
	hold.j.deliver("remote-agent", "local-agent", hold.now.Add(time.Hour))
	require.NoError(t, hold.proc.Scan(t.Context()))
	require.Equal(t, "reply-hold", hold.sched.Decide().Reason)
}

// heldState is a stored accounting record with a fixed accrual mark.
func heldState(now time.Time, accountedAgo time.Duration, stored int64, holds ...oplocal.ReplyHold) oplocal.ReplyState {
	return oplocal.ReplyState{Day: now.UTC().Format("2006-01-02"), HeldSecs: stored, AccountedTo: now.Add(-accountedAgo), Holds: holds}
}

func TestReplyStatusAt_Accrual_Table(t *testing.T) {
	now := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)
	hold := func(startAgo, untilIn time.Duration) oplocal.ReplyHold {
		return oplocal.ReplyHold{Sender: "s", Start: now.Add(-startAgo), Until: now.Add(untilIn)}
	}
	tests := []struct {
		name string
		st   oplocal.ReplyState
		want int64
	}{
		{"overlapping holds count once", heldState(now, 100*time.Second, 0, hold(200*time.Second, time.Hour), hold(150*time.Second, time.Hour)), 100},
		{"accrual adds to the stored seconds", heldState(now, 60*time.Second, 10, hold(time.Hour, time.Hour)), 70},
		{"a gap longer than nine hours accrues at most nine", heldState(now, 12*time.Hour, 0, hold(13*time.Hour, time.Hour)), 9 * 3600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := service.ReplyStatusAt(tt.st, service.ReplyPolicy{}, now)
			require.Equal(t, tt.want, got.RemoteHeldSecsToday)
		})
	}
}
