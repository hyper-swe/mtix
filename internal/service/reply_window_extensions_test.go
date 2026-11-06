// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// extend delivers one reply_by event from sender at the fixture's clock and
// runs one dial so the scan sees it.
func (f *replyFixture) extend(sender string, replyIn time.Duration) {
	f.j.deliver(sender, "local-agent", f.now.Add(replyIn))
	f.activity = f.now // the host is pulling: a dial happens at this instant
	f.sched.MarkContact()
	f.sched = service.NewContactScheduler(func() time.Time { return f.now }, f.inputs)
	f.dials++
	require.NoError(f.t, f.proc.Scan(f.t.Context()))
}

func TestReplyExtensions_LimitUnset_Unlimited(t *testing.T) {
	f := activeAt(t)
	for i := 0; i < 12; i++ {
		f.extend("chatty", time.Hour)
		f.now = f.now.Add(time.Minute)
	}
	require.Len(t, f.replyState().Holds, 12, "with no limit every extension holds")
}

func TestReplyExtensions_LimitSet_NextExtensionDoesNotHold(t *testing.T) {
	f := activeAt(t)
	f.vals[service.ReplyExtensionsKey] = "2"
	f.extend("chatty", 10*time.Minute)
	f.extend("chatty", 10*time.Minute)
	require.Len(t, f.replyState().Holds, 2)
	f.extend("chatty", 8*time.Hour) // the third in the hour: past the limit
	require.Len(t, f.replyState().Holds, 2, "the next extension past the limit does not hold")
	f.extend("other", 10*time.Minute)
	require.Len(t, f.replyState().Holds, 3, "the limit is per sender")

	// The limit rolls: 61 minutes later the same sender may extend again.
	f.now = f.now.Add(61 * time.Minute)
	require.NoError(t, f.proc.Tick(t.Context()))
	f.extend("chatty", 30*time.Minute)
	holds := f.replyState().Holds
	require.Len(t, holds, 1, "expired holds dropped; the new extension holds")
	require.Equal(t, "chatty", holds[0].Sender)
}

func TestReplyExtensions_LimitSet_RefusedExtensionDoesNotKeepPulling(t *testing.T) {
	f := activeAt(t)
	f.vals[service.ReplyExtensionsKey] = "1"
	f.j.deliver("chatty", "local-agent", t0.Add(15*time.Minute))
	f.j.deliver("chatty", "local-agent", t0.Add(8*time.Hour))
	f.run(3 * time.Hour)
	require.Zero(t, f.dialsBetween(t0.Add(31*time.Minute), f.now), "only the first extension held, and it ended at 15 minutes")
}

func TestReadReplyPolicy_Table_UnsetSetAndInvalid(t *testing.T) {
	f := activeAt(t)
	pol, err := service.ReadReplyPolicy(f)
	require.NoError(t, err)
	require.Equal(t, service.ReplyPolicy{}, pol, "unset means unlimited")
	f.vals[service.ReplyExtensionsKey], f.vals[service.RemoteAwakeCapKey] = "3", "900"
	pol, err = service.ReadReplyPolicy(f)
	require.NoError(t, err)
	require.Equal(t, service.ReplyPolicy{ExtensionsPerHour: 3, DailyCapSecs: 900}, pol)
	f.vals[service.RemoteAwakeCapKey] = "abc"
	_, err = service.ReadReplyPolicy(f)
	require.Error(t, err)
}
