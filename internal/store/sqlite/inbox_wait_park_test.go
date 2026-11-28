// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-328: the no-policy InboxWait parks on the database change token.
package sqlite

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// countWaitReads counts the queries a parked wait runs.
func countWaitReads(s *Store) *atomic.Int64 {
	n := &atomic.Int64{}
	s.onWaitRead = func() { n.Add(1) }
	return n
}

// countTokenStats counts the change-token reads a wait makes: one per stat of
// the database file itself (each token also stats its -wal and -shm).
func countTokenStats(s *Store) *atomic.Int64 {
	n := &atomic.Int64{}
	s.tokenStat = func(name string) (fs.FileInfo, error) {
		if name == s.dbPath {
			n.Add(1)
		}
		return os.Stat(name)
	}
	return n
}

// Acceptance: an idle wait runs no query between safety reads.
func TestInboxWait_IdleUnchangedDB_NoQueryAfterEntry(t *testing.T) {
	s := inboxWaitStore(t)
	reads := countWaitReads(s)
	events, err := s.InboxWait(t.Context(), "worker", 1300*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.EqualValues(t, 1, reads.Load(), "the entry query only: five 250 ms ticks ask the database nothing")
}

// Acceptance: a registered session's entry touch commits after the token is
// taken, so the wait runs one more query on the next tick (a bounded extra read,
// no touch) and then parks: at most two queries before the safety floor.
func TestInboxWait_IdleRegisteredSession_AtMostTwoQueriesBeforeFloor(t *testing.T) {
	s, _ := touchWaitStore(t)
	reads := countWaitReads(s)
	ctx := WithSessionTouch(t.Context(), SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"})
	events, err := s.InboxWait(ctx, "w", 1300*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.GreaterOrEqual(t, reads.Load(), int64(1))
	assert.LessOrEqual(t, reads.Load(), int64(2), "the entry read and at most one follow-up for its own touch")
}

// Acceptance: the safety read still runs once per floor.
func TestInboxWait_IdleUnchangedDB_SafetyReadPerFloor(t *testing.T) {
	s := inboxWaitStore(t)
	s.waitFloor = 600 * time.Millisecond
	reads := countWaitReads(s)
	_, err := s.InboxWait(t.Context(), "worker", 1800*time.Millisecond)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, reads.Load(), int64(2), "a safety read ran")
	assert.LessOrEqual(t, reads.Load(), int64(4), "about one per floor")
}

// Acceptance: touches commit at most once per InboxReadThrottle. An unrelated
// commit by another process moves the token, costs one query and no touch.
func TestInboxWait_UnrelatedCommit_OneQueryNoTouch(t *testing.T) {
	s := inboxWaitStore(t)
	writer, err := New(s.dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, writer.Close()) }()
	reads := countWaitReads(s)
	var clockCalls atomic.Int64
	s.SetClock(func() time.Time { clockCalls.Add(1); return inboxWaitTime() })
	ctx := WithSessionTouch(t.Context(), SessionTouch{HostID: "h", UID: "u", Handle: "sess"})
	out := startInboxWait(ctx, t, s, 1500*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	atEntry := clockCalls.Load()
	require.NoError(t, writer.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
		ID: "other", Author: "x", Text: "not for worker", Addressee: "someone-else", CreatedAt: inboxWaitTime(),
	}}))
	time.Sleep(500 * time.Millisecond) // the moved token is seen within a tick; the wait is still open
	assert.EqualValues(t, 2, reads.Load(), "entry query plus one for the moved token")
	assert.Equal(t, atEntry, clockCalls.Load(), "the re-read carried no session touch")
	require.NoError(t, (<-out).err)
}

// seenIs reports whether the registration's last_seen is want (a non-failing
// read, so a polling test can call it from require.Eventually).
func seenIs(s *Store, want string) bool {
	rows, err := s.ListRegistrations(context.Background(), "host-a", "501")
	return err == nil && len(rows) == 1 && rows[0].LastSeen == want
}

// MTIX-328 (FR-19.4): a parked store read touches the session again once
// InboxReadThrottle has passed on the wait's monotonic clock, and not before.
// The store clock and the wait's monotonic clock are advanced by the test; the
// safety floor is short so each advance is followed by one parked read.
func TestInboxWait_ParkedRead_TouchesSessionOnlyAfterThrottle(t *testing.T) {
	s, _ := touchWaitStore(t)
	base := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	monoBase := time.Now()
	var shift atomic.Int64 // seconds both clocks have moved since entry
	s.SetClock(func() time.Time { return base.Add(time.Duration(shift.Load()) * time.Second) })
	s.waitMono = func() time.Time { return monoBase.Add(time.Duration(shift.Load()) * time.Second) }
	s.waitFloor = 300 * time.Millisecond
	reads, stats := countWaitReads(s), countTokenStats(s)
	stamp := func(sec int) string { return base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339) }
	ctx, cancel := context.WithCancel(WithSessionTouch(t.Context(), SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"}))
	done := make(chan error, 1)
	go func() { _, err := s.InboxWait(ctx, "w", 0); done <- err }()
	t.Cleanup(func() { cancel(); <-done })
	// idle waits until the n-th read has run and the wait has parked again: a
	// later token stat can only come from a tick after that read finished.
	idle := func(n int64) {
		require.Eventually(t, func() bool { return reads.Load() == n }, 5*time.Second, 5*time.Millisecond)
		seen := stats.Load()
		require.Eventually(t, func() bool { return stats.Load() > seen }, 5*time.Second, 5*time.Millisecond)
	}

	require.Eventually(t, func() bool { return seenIs(s, stamp(0)) }, 5*time.Second, 5*time.Millisecond, "the entry read touches")
	idle(2) // the follow-up read of the entry touch's own commit carries no touch
	shift.Store(10)
	idle(3) // a safety read 10 s later, below InboxReadThrottle
	assert.True(t, seenIs(s, stamp(0)), "a parked read inside the throttle must not touch the session")
	shift.Store(40)
	idle(4) // a safety read 40 s after the last touch
	assert.True(t, seenIs(s, stamp(40)), "a parked read past the throttle touches the session again")
}

// MTIX-328 acceptance 3 (FR-19.4): a parked store wait wakes within 2 s of a
// new addressed event written by another process, not at the 60 s safety read.
func TestInboxWait_ParkedWait_AddressedEventWakesWithinTwoSeconds(t *testing.T) {
	s := inboxWaitStore(t)
	writer, err := New(s.dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, writer.Close()) }()
	stats := countTokenStats(s)
	out := startInboxWait(t.Context(), t, s, 0)
	require.Eventually(t, func() bool { return stats.Load() >= 2 }, 5*time.Second, 5*time.Millisecond, "the entry token and a parked tick")
	began := time.Now()
	require.NoError(t, writer.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
		ID: "latency", Author: "sender", Text: "new event", Addressee: "worker", CreatedAt: inboxWaitTime(),
	}}))
	select {
	case result := <-out:
		require.NoError(t, result.err)
		require.Len(t, result.events, 1)
		assert.Less(t, time.Since(began), 2*time.Second, "a commit moves the token and the next tick reads")
	case <-time.After(10 * time.Second):
		t.Fatal("the parked wait did not wake for the new event")
	}
}
