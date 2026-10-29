// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package oplocal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHubActivity_RecordThenRead_RoundTrips(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/a/.mtix")
	_, present, err := s.ReadHubActivity(key)
	require.NoError(t, err)
	require.False(t, present)

	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.RecordHubActivity(key, HubActivity{At: at, Kind: ActivityMutation}))
	got, present, err := s.ReadHubActivity(key)
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, at.Equal(got.At))
	require.Equal(t, ActivityMutation, got.Kind)
}

func TestHubActivity_UnknownKind_Refused(t *testing.T) {
	s := fixture(t)
	err := s.RecordHubActivity(BoardKey("/work/a/.mtix"), HubActivity{At: time.Now(), Kind: "parked_wait"})
	require.Error(t, err)
}

func TestHubActivity_OtherBoard_NotShared(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.RecordHubActivity(BoardKey("/work/a/.mtix"),
		HubActivity{At: time.Now(), Kind: ActivityTurn}))
	_, present, err := s.ReadHubActivity(BoardKey("/work/b/.mtix"))
	require.NoError(t, err)
	require.False(t, present)
}

func TestListenLease_AnyArmOrRearm_NeverTouchesActivity(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/a/.mtix")
	start := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.RecordHubActivity(key, HubActivity{At: start, Kind: ActivityGlue}))

	// First waiter arms, refreshes, then exits.
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 11, StartTime: "a", ArmedAt: start.Add(time.Minute)}))
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 11, StartTime: "a", ArmedAt: start.Add(time.Hour)}))
	require.NoError(t, s.RemoveListenLease(key, 11))
	// A second waiter process arms long after (past an 8h clamp).
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 12, StartTime: "b", ArmedAt: start.Add(9 * time.Hour)}))
	// A reused pid with a new start time is just another arm.
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 12, StartTime: "c", ArmedAt: start.Add(10 * time.Hour)}))

	got, present, err := s.ReadHubActivity(key)
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, start.Equal(got.At), "no waiter arm moves the activity window")
}

func TestListenLease_ArmWithNoActivity_RecordsNothing(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/a/.mtix")
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 4242, StartTime: "x1", ArmedAt: time.Now()}))
	_, present, err := s.ReadHubActivity(key)
	require.NoError(t, err)
	require.False(t, present)
}

func TestListenLease_ListAndRemove(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/a/.mtix")
	other := BoardKey("/work/b/.mtix")
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 11, StartTime: "s11", ArmedAt: time.Now()}))
	require.NoError(t, s.WriteListenLease(key, ListenLease{PID: 12, StartTime: "s12", ArmedAt: time.Now()}))
	require.NoError(t, s.WriteListenLease(other, ListenLease{PID: 13, StartTime: "s13", ArmedAt: time.Now()}))

	leases, err := s.ListListenLeases(key)
	require.NoError(t, err)
	require.Len(t, leases, 2)

	require.NoError(t, s.RemoveListenLease(key, 11))
	leases, err = s.ListListenLeases(key)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.Equal(t, 12, leases[0].PID)
	require.NoError(t, s.RemoveListenLease(key, 11), "removing an absent lease is not an error")
}

func TestListenLease_InvalidLease_Refused(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/x/.mtix")
	require.Error(t, s.WriteListenLease(key, ListenLease{PID: 0, StartTime: "s", ArmedAt: time.Now()}))
	require.Error(t, s.WriteListenLease(key, ListenLease{PID: 5, ArmedAt: time.Now()}))
	require.Error(t, s.WriteListenLease(key, ListenLease{PID: 5, StartTime: "s"}))
}

func TestHubContactRecord_RoundTrips(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/a/.mtix")
	at := time.Date(2026, 10, 28, 9, 5, 0, 0, time.UTC)
	require.NoError(t, s.RecordHubContact(key, HubContact{At: at, Route: "poll", OK: true}))
	got, present, err := s.ReadHubContact(key)
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, at.Equal(got.At))
	require.Equal(t, "poll", got.Route)
}

func TestBoardKey_StableAndDistinct(t *testing.T) {
	require.Equal(t, BoardKey("/a/.mtix"), BoardKey("/a/.mtix"))
	require.NotEqual(t, BoardKey("/a/.mtix"), BoardKey("/b/.mtix"))
	require.Regexp(t, "^[0-9a-f]{16}$", BoardKey("/a/.mtix"))
}
