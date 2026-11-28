// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hookParkSeedSeen is the last_seen a test registration starts with; the wait's
// entry touch moves it, which is how a test sees that the entry read is done.
const hookParkSeedSeen = "2000-01-01T00:00:00Z"

// startParkedHookWait registers the worker, starts claudeHookWait (no timeout,
// nothing skipped) and returns once the wait is idle: its entry touch is
// committed and the change token has stayed the same across two checks, each
// longer than one 250 ms tick (MTIX-328, FR-19.4).
func startParkedHookWait(t *testing.T) parkedHookWait {
	t.Helper()
	node := wakeStatusApp(t)
	host, uid := seededWakeHost(t)
	_, err := app.store.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: "worker", HostID: host, UID: uid},
		func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "worker", HostID: host, UID: uid, SessionHandle: "hook-sess",
				Harness: "claude", Origin: "test", RegisteredAt: hookParkSeedSeen, LastSeen: hookParkSeedSeen,
				RegistrarVersion: "test", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(sqlite.WithSessionTouch(t.Context(), sqlite.SessionTouch{HostID: host, UID: uid, Handle: "hook-sess"}))
	join := func(string, io.Writer) func() { return func() {} }
	done := make(chan hookWaitResult, 1)
	go func() {
		got, werr := claudeHookWait(ctx, "worker", 0, func(sqlite.InboxEvent) bool { return false }, join)
		done <- hookWaitResult{len(got), werr}
	}()
	t.Cleanup(cancel)
	require.Eventually(t, func() bool {
		rows, lerr := app.store.ListRegistrations(t.Context(), host, uid)
		return lerr == nil && len(rows) == 1 && rows[0].LastSeen != hookParkSeedSeen
	}, 10*time.Second, 20*time.Millisecond, "the entry read touches the session")
	return parkedHookWait{node: node, token: awaitQuietToken(t), done: done, cancel: cancel}
}

// parkedHookWait is an idle claudeHookWait: the board node, the settled change
// token, the result channel and the cancel of the wait.
type parkedHookWait struct {
	node   string
	token  string
	done   <-chan hookWaitResult
	cancel context.CancelFunc
}

type hookWaitResult struct {
	n   int
	err error
}

// awaitQuietToken polls the change token until two checks 300 ms apart agree,
// so the entry read, its touch commit and the follow-up count are all done.
func awaitQuietToken(t *testing.T) string {
	t.Helper()
	prev, err := app.store.InboxChangeToken(t.Context())
	require.NoError(t, err)
	for range 40 {
		time.Sleep(300 * time.Millisecond)
		cur, cerr := app.store.InboxChangeToken(t.Context())
		require.NoError(t, cerr)
		if cur == prev {
			return cur
		}
		prev = cur
	}
	t.Fatal("the change token never settled")
	return ""
}

// MTIX-328 acceptance: an idle Claude hook wait commits nothing to the board
// database after its entry read. The store's change token is the file stat, so
// an unchanged token over several old 250 ms polls proves no commit happened
// (the old waiter touched the session on every poll). The baseline is taken
// from observable state (the entry touch, then a settled token), not a sleep.
func TestClaudeHookWait_Idle_CommitsNothingAfterEntry(t *testing.T) {
	w := startParkedHookWait(t)
	time.Sleep(1200 * time.Millisecond) // about five old polls
	after, err := app.store.InboxChangeToken(t.Context())
	require.NoError(t, err)
	assert.Equal(t, w.token, after, "an idle hook wait must not commit between safety reads")
	w.cancel()
	res := <-w.done
	require.NoError(t, res.err)
	assert.Zero(t, res.n)
}

// MTIX-328 acceptance 3 (FR-19.4): a parked hook wait on the real store wakes
// within 2 s of a new addressed event, not at the 60 s safety read.
func TestClaudeHookWait_ParkedWait_NewMailWakesWithinTwoSeconds(t *testing.T) {
	w := startParkedHookWait(t)
	began := time.Now()
	addressHookMail(t, w.node, "hook-wake-latency", "new mail")
	select {
	case res := <-w.done:
		require.NoError(t, res.err)
		assert.Equal(t, 1, res.n)
		assert.Less(t, time.Since(began), 2*time.Second, "a commit moves the token and the next tick reads")
	case <-time.After(10 * time.Second):
		t.Fatal("the parked hook wait did not wake for new mail")
	}
}

// MTIX-328 (FR-19.4): a signal during the park still reports the held count on
// stderr, as the 250 ms loop did before the park.
func TestClaudeHookWait_SignalDuringPark_ReportsHeldCount(t *testing.T) {
	heldBoard(t, 2) // a hub-connected board with an unconfirmed id: both events are held
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(600*time.Millisecond, cancel)
	var res hookWaitResult
	stderr := captureStderr(t, func() {
		got, err := claudeHookWait(ctx, policyAgent, 0, func(sqlite.InboxEvent) bool { return false }, func(string, io.Writer) func() { return func() {} })
		res = hookWaitResult{len(got), err}
	})
	require.NoError(t, res.err)
	assert.Zero(t, res.n)
	assert.Contains(t, stderr, "2 held", "a signalled wait prints the held notice")
}
