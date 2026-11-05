// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// activeAt starts a fixture whose first pass is a dial inside a fresh activity
// window, which is how a real host fetches the event it will hold for.
func activeAt(t *testing.T) *replyFixture { return newReplyFixture(t, t0, t0) }

func TestRecipientHold_AnySender_HoldsUntilClampedDeadline(t *testing.T) {
	tests := []struct {
		name    string
		replyBy time.Duration
		holdFor time.Duration
	}{
		{"deadline before the clamp", 2 * time.Hour, 2 * time.Hour},
		{"deadline past eight hours clamps to fetched_at+8h", 20 * time.Hour, 8 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := activeAt(t)
			f.j.deliver("stranger", "local-agent", t0.Add(tt.replyBy)) // any sender, no allowlist (C2)
			f.run(tt.holdFor + 30*time.Minute)
			end := t0.Add(tt.holdFor)
			require.NotZero(t, f.dialsBetween(end.Add(-2*time.Minute), end), "still pulling just before the deadline")
			require.Zero(t, f.dialsBetween(end.Add(time.Minute), f.now), "no pull after the clamped deadline")
			for i := range f.dialTimes[1:] {
				if cur := f.dialTimes[i+1]; cur.After(t0.Add(30 * time.Minute)) {
					require.Equal(t, 60*time.Second, cur.Sub(f.dialTimes[i]), "60 s pulls once the activity window is over")
				}
			}
		})
	}
}

func TestRecipientHold_NotAddressedToThisHost_DoesNotHold(t *testing.T) {
	tests := []struct {
		name, author, to string
	}{
		{"agent unregistered anywhere", "stranger", "nobody"},
		{"agent registered on another host", "stranger", "far-agent"},
		{"event authored by the addressee itself", "local-agent", "local-agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := activeAt(t)
			f.j.deliver(tt.author, tt.to, t0.Add(6*time.Hour))
			f.run(2 * time.Hour)
			require.Empty(t, f.replyState().Holds)
			require.Zero(t, f.dialsBetween(t0.Add(31*time.Minute), f.now), "pulls stop with the activity window")
		})
	}
}

func TestRecipientHold_PastDeadline_DoesNotHold(t *testing.T) {
	f := activeAt(t)
	f.j.deliver("stranger", "local-agent", t0.Add(-time.Minute))
	f.run(time.Hour)
	require.Empty(t, f.replyState().Holds)
}

func TestRecipientHold_DailyCapUnset_Uncapped(t *testing.T) {
	f := activeAt(t)
	f.j.deliver("stranger", "local-agent", t0.Add(8*time.Hour))
	f.run(8*time.Hour + 10*time.Minute)
	require.NotZero(t, f.dialsBetween(t0.Add(7*time.Hour+50*time.Minute), t0.Add(8*time.Hour)))
	status := service.ReplyStatusAt(f.replyState(), service.ReplyPolicy{}, f.now)
	require.Zero(t, status.DailyCapSecs)
	require.False(t, status.CapSpent)
	require.Greater(t, status.RemoteHeldSecsToday, int64(7*3600), "all eight held hours are counted, none refused")
}

func TestRecipientHold_DailyCapSet_HoldEndsWhenSpent(t *testing.T) {
	f := activeAt(t)
	f.vals[service.RemoteAwakeCapKey] = "3600" // one hour of remote-held time a day
	f.j.deliver("stranger", "local-agent", t0.Add(8*time.Hour))
	f.run(4 * time.Hour)
	spentAt := t0.Add(time.Hour)
	require.NotZero(t, f.dialsBetween(spentAt.Add(-5*time.Minute), spentAt), "held right up to the cap")
	require.Zero(t, f.dialsBetween(spentAt.Add(time.Minute), f.now), "the hold ends when the day's cap is spent")
	require.True(t, service.ReplyStatusAt(f.replyState(), service.ReplyPolicy{DailyCapSecs: 3600}, f.now).CapSpent)
	// A new reply_by event the same UTC day does not hold either.
	before := f.dials
	f.j.deliver("other", "local-agent", f.now.Add(2*time.Hour))
	f.activity = f.now // a pull happens (activity), the scan sees the event
	f.run(20 * time.Minute)
	require.Empty(t, f.replyState().Holds, "no new hold once the cap is spent")
	require.Greater(t, f.dials, before, "the activity window itself still pulls")
}

func TestRecipientHold_DailyCapRollsOverAtUTCMidnight(t *testing.T) {
	start := time.Date(2026, 10, 28, 23, 50, 0, 0, time.UTC)
	f := newReplyFixture(t, start, start)
	f.vals[service.RemoteAwakeCapKey] = "1200" // 20 minutes a UTC day
	f.j.deliver("stranger", "local-agent", start.Add(3*time.Hour))
	f.run(time.Hour)
	// 10 minutes before midnight count for the 28th; the 29th starts at zero,
	// so the hold lasts until 00:10 + 20 min = 00:20 rather than ending at 00:10.
	cutoff := time.Date(2026, 10, 29, 0, 20, 0, 0, time.UTC)
	require.NotZero(t, f.dialsBetween(cutoff.Add(-5*time.Minute), cutoff))
	require.Zero(t, f.dialsBetween(cutoff.Add(time.Minute), f.now))
}
