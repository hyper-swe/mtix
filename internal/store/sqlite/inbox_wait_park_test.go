// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-328: the no-policy InboxWait parks on the database change token.
package sqlite

import (
	"log/slog"
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

// Acceptance: an idle wait runs no query between safety reads.
func TestInboxWait_IdleUnchangedDB_NoQueryAfterEntry(t *testing.T) {
	s := inboxWaitStore(t)
	reads := countWaitReads(s)
	events, err := s.InboxWait(t.Context(), "worker", 1300*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.EqualValues(t, 1, reads.Load(), "the entry query only: five 250 ms ticks ask the database nothing")
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
