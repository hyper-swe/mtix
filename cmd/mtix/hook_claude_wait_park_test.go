// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-328 acceptance: an idle Claude hook wait commits nothing to the board
// database after its entry read. The store's change token is the file stat, so
// an unchanged token over several old 250 ms polls proves no commit happened
// (the old waiter touched the session on every poll).
func TestClaudeHookWait_Idle_CommitsNothingAfterEntry(t *testing.T) {
	wakeStatusApp(t)
	host, uid := seededWakeHost(t)
	_, err := app.store.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: "worker", HostID: host, UID: uid},
		func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "worker", HostID: host, UID: uid, SessionHandle: "hook-sess",
				Harness: "claude", Origin: "test", RegisteredAt: "2026-10-29T00:00:00Z", LastSeen: "2026-10-29T00:00:00Z",
				RegistrarVersion: "test", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
	ctx := sqlite.WithSessionTouch(t.Context(), sqlite.SessionTouch{HostID: host, UID: uid, Handle: "hook-sess"})
	join := func(string, io.Writer) func() { return func() {} }
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, werr := claudeHookWait(ctx, "worker", 1500*time.Millisecond, func(sqlite.InboxEvent) bool { return false }, join)
		done <- result{len(got), werr}
	}()
	time.Sleep(400 * time.Millisecond) // entry read and its touch are done
	before, err := app.store.InboxChangeToken(t.Context())
	require.NoError(t, err)
	res := <-done
	require.NoError(t, res.err)
	assert.Zero(t, res.n)
	after, err := app.store.InboxChangeToken(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, after, "an idle hook wait must not commit between safety reads")
}
