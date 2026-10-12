// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Real producer witnesses use controlled cadence, a real inbox adapter and
// kernel contention. No shell adapter or sleep supplies progress evidence.
package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/stretchr/testify/require"
)

func TestDispatchLock_HeldByOther_DaemonIdlesNotExits(t *testing.T) {
	cases := []struct {
		name string
		sync bool
	}{{"daemon", false}, {"syncdaemon", true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { testDispatchLockProducer(t, tc.sync) })
	}
}

func testDispatchLockProducer(t *testing.T, syncSpelling bool) {
	dir, store := dispatchLockFixture(t)
	lock, err := pushlock.AcquireNamed(dir, "dispatch.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	observed := make(chan bool, 1)
	done := make(chan error, 1)
	control := daemonPassControl{ticks: ticks, observed: func(acquired bool) {
		select {
		case observed <- acquired:
		case <-ctx.Done():
		}
	}}
	var stdout, stderr bytes.Buffer
	go func() {
		if syncSpelling {
			done <- runSyncDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, true, control)
		} else {
			done <- runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, control)
		}
	}()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			awaitDispatchDone(t, done)
		}
	})
	require.False(t, awaitDispatchObservation(t, observed))
	assertDispatchHeld(t, store, done, dir)
	sendDispatchTick(t, ticks, done)
	require.False(t, awaitDispatchObservation(t, observed), "held lock retries and remains idle")
	assertDispatchHeld(t, store, done, dir)
	require.NoError(t, lock.Release())
	sendDispatchTick(t, ticks, done)
	require.True(t, awaitDispatchObservation(t, observed), "released lock permits a real pass")
	assertDispatchDelivered(t, store, dir)
	cancel()
	awaitDispatchDone(t, done)
	joined = true
	require.Contains(t, stdout.String(), "shutting down")
	_, err = os.Stat(filepath.Join(dir, daemonPIDFilename))
	require.ErrorIs(t, err, os.ErrNotExist)
	owner, err := acquireDaemonOwnership(dir)
	require.NoError(t, err, "cancelled producer releases lifetime ownership")
	require.NoError(t, owner.release())
}

func dispatchLockFixture(t *testing.T) (string, *sqlite.Store) {
	t.Helper()
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	t.Setenv(transport.EnvDSN, "")
	t.Setenv("MTIX_PG_TEST_DSN", "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte("hooks:\n  - name: dispatch-lock-worker\n    match:\n      events: [comment.addressed]\n      to-agent: worker\n    deliver: [inbox]\n"), 0600))
	_, err := store.WriteDB().Exec(`INSERT INTO sync_events (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "dispatch-lock-event", "TEST", "TEST-1", "comment", `{"to":"worker","text":"dispatch after owner releases"}`, 1, 1, "{}", "poster", "0123456789abcdef", "pending", "2026-10-12T00:00:00Z")
	require.NoError(t, err)
	saved := app
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app = appContext{mtixDir: dir, store: store, logger: logger, hooksDisp: service.NewHooksDispatcher(store, dir, logger)}
	t.Cleanup(func() { app = saved })
	return dir, store
}

func assertDispatchHeld(t *testing.T, store *sqlite.Store, done <-chan error, dir string) {
	t.Helper()
	entries, err := store.ReadHookLog(context.Background(), 100)
	require.NoError(t, err)
	require.Empty(t, entries)
	select {
	case err := <-done:
		t.Fatalf("contended producer exited: %v", err)
	default:
	}
	pid, err := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(os.Getpid()), string(pid))
}

func awaitDispatchObservation(t *testing.T, observed <-chan bool) bool {
	t.Helper()
	select {
	case result := <-observed:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("producer pass did not join observation")
		return false
	}
}
func awaitDispatchDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("producer cancellation did not join")
	}
}

func assertDispatchDelivered(t *testing.T, store *sqlite.Store, dir string) {
	t.Helper()
	entries, err := store.ReadHookLog(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "dispatch-lock-worker", entries[0].Hook)
	require.Equal(t, "delivered", entries[0].Outcome)
	next, err := pushlock.AcquireNamed(dir, "dispatch.lock")
	require.NoError(t, err, "pass releases lock before cadence wait")
	require.NoError(t, next.Release())
}

func sendDispatchTick(t *testing.T, ticks chan<- time.Time, done <-chan error) {
	t.Helper()
	select {
	case ticks <- time.Time{}:
	case err := <-done:
		t.Fatalf("producer exited before controlled tick: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("producer did not accept controlled tick")
	}
}
