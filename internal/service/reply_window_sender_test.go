// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAwaitReply_WindowKeeps60sPulls(t *testing.T) {
	f := newReplyFixture(t, t0, t0.Add(-3*time.Hour)) // activity window long closed
	f.openWindow("peer", 10*time.Minute)
	f.run(10 * time.Minute)
	require.Equal(t, 10, f.dials, "one pull a minute while the window is open: minutes 0..9")
	for i := 1; i < len(f.dialTimes); i++ {
		require.Equal(t, 60*time.Second, f.dialTimes[i].Sub(f.dialTimes[i-1]))
	}
	f.run(time.Hour)
	require.Equal(t, 10, f.dials, "the TTL closed the window: no pull after it")
}

func TestAwaitReply_ReplyArrives_WindowCloses(t *testing.T) {
	f := newReplyFixture(t, t0, t0.Add(-3*time.Hour))
	f.openWindow("peer", 2*time.Hour)
	f.run(3 * time.Minute)
	require.Equal(t, 3, f.dials)
	f.j.answered["peer>me"] = true // the awaited agent replied; the next pull's scan sees it
	f.run(time.Hour)
	require.Equal(t, 4, f.dials, "one more pull (the one that fetched the reply), then none")
	require.Empty(t, f.replyState().Windows)
}

func TestAwaitReply_OtherAgentsReply_DoesNotClose(t *testing.T) {
	f := newReplyFixture(t, t0, t0.Add(-3*time.Hour))
	f.openWindow("peer", 5*time.Minute)
	f.j.answered["stranger>me"] = true
	f.run(5 * time.Minute)
	require.Equal(t, 5, f.dials)
}

func TestAwaitReply_TTLClampedTo8h(t *testing.T) {
	d, err := model.ParseAwaitReply("100h")
	require.NoError(t, err)
	require.Equal(t, 8*time.Hour, d)
	f := newReplyFixture(t, t0, t0.Add(-3*time.Hour))
	f.openWindow("peer", d)
	f.run(8*time.Hour + 30*time.Minute)
	require.Equal(t, 480, f.dials, "60 s pulls for exactly eight hours, none after")
	require.Zero(t, f.dialsBetween(t0.Add(8*time.Hour), f.now))
}

func TestContactCadence_NoWindowsNoHolds_Unchanged(t *testing.T) {
	f := newReplyFixture(t, t0, t0) // fresh activity, no reply windows or holds
	f.run(30 * time.Minute)
	require.InDelta(t, 140, f.dials, 3, "fast phase 120 plus slow phase 20, exactly the activity cadence")
	closed := f.dials
	f.run(24 * time.Hour)
	require.Equal(t, closed, f.dials, "scale-to-zero: no pull at all once the window closes")
	require.Zero(t, f.j.regCalls, "no reply events means no registration lookup either")
	require.Empty(t, f.replyState().Holds)
	require.Empty(t, f.replyState().Windows)
}

func TestContactCadence_ManualMode_IgnoresReplyWindow(t *testing.T) {
	f := newReplyFixture(t, t0, t0)
	f.openWindow("peer", time.Hour)
	in, err := f.inputs()
	require.NoError(t, err)
	in.Mode.Kind = 3 // manual
	require.True(t, in.Reply.WindowOpen)
	b := newFakeBoard(in)
	b.run(time.Hour)
	require.Zero(t, b.dials, "manual never contacts the hub, even inside a reply window")
}
