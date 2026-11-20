// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const (
	idleWaiterAgent  = "waiter-a"
	idleWaiterHandle = "idle-waiter-handle"
	idleHookName     = "idle-test"
)

// idleBookkeeping replays, under a running daemon, every local bookkeeping write
// the wake scanner and hook dispatcher make in the lab shape: a registered
// waiter session refreshing last_seen, a wake claim, a wake scan cursor advance
// and a hook dispatch claim. MTIX-130.20.3.1: each of these must persist AND
// leave no pending export request; the notifying transaction would record one.
type idleBookkeeping struct {
	hostID, uid string
	dir         string
}

// seedIdleBookkeeping registers a live waiter session with an old last_seen and
// writes a hook that matches the seeded addressed comment, so the daemon's own
// dispatcher also claims and delivers it through the real path.
func seedIdleBookkeeping(t *testing.T, hostID, uid string) idleBookkeeping {
	t.Helper()
	pid := os.Getpid()
	start, err := procinfo.StartTime(pid)
	require.NoError(t, err)
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	// A live registration with a session handle: only that handle may touch last_seen.
	_, err = app.store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,pid,pid_start_time,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol,session_handle)
		VALUES (?,?,?,?,?,?,?,1,0,0,?,?,'test',1,?)`,
		idleWaiterAgent, "codex", uid, hostID, pid, start, "manual", old, old, idleWaiterHandle)
	require.NoError(t, err)
	hooks := "hooks:\n  - name: " + idleHookName + "\n    match:\n      events: [comment.addressed]\n      to-agent: dead-agent\n    deliver: [inbox]\n"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(hooks), 0o600))
	return idleBookkeeping{hostID: hostID, uid: uid, dir: app.mtixDir}
}

// afterTick runs the local bookkeeping writes once and asserts no export request
// exists once they have committed (the request is recorded synchronously).
func (b idleBookkeeping) afterTick(t *testing.T, tick int) {
	t.Helper()
	ctx := sqlite.WithSessionTouch(t.Context(), sqlite.SessionTouch{HostID: b.hostID, UID: b.uid, Handle: idleWaiterHandle})
	_, err := app.store.InboxList(ctx, idleWaiterAgent) // refreshes the registered session's last_seen
	require.NoError(t, err)
	key := model.WakeKey{AgentID: idleWaiterAgent, HostID: b.hostID, UID: b.uid, EventSeq: int64(100 + tick), Epoch: 1}
	_, won, err := app.store.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won, "tick %d: the wake claim must be won", tick)
	// An open claim above the journal tail is never pruned, so its row stays observable.
	claimed, err := app.store.ClaimHookDispatch(t.Context(), idleHookName, int64(1000+tick), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed, "tick %d: the hook dispatch claim must be won", tick)
	require.NoError(t, app.store.AdvanceWakeScanCursor(t.Context(), b.hostID, b.uid, 1, time.Minute))
	requireNoPendingExport(t, b.dir, tick)
}

// requireIdleBookkeepingPersisted pins fixture coverage as well as export
// silence: without it a missing or no-op write would let the regression pass.
func requireIdleBookkeepingPersisted(t *testing.T) {
	t.Helper()
	tests := []struct{ name, query string }{
		{"registration touch", "SELECT count(*) FROM agent_registrations WHERE agent_id='waiter-a' AND last_seen > registered_at"},
		{"wake claim", "SELECT count(*) FROM wake_ledger WHERE agent_id='waiter-a'"},
		{"wake cursor", "SELECT count(*) FROM wake_scan_cursor WHERE cursor > 0"},
		{"hook claim", "SELECT count(*) FROM hook_dispatch_ledger WHERE hook_name='idle-test'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var count int
			// Each literal query proves the named local-only write persisted.
			require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(), tt.query).Scan(&count))
			require.Positive(t, count, "fixture never exercised %s", tt.name)
		})
	}
}
