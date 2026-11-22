// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// MTIX-130.20.3: the daemon's own ticks must not create export work. The real
// daemon wires the mirror exporter's on-commit hook; an idle board must see no
// pending export request after any number of ticks, and no export pass may
// ever open sync.lock.
func TestDaemon_IdleTicksRecordNoPendingExportRequest(t *testing.T) {
	dir, advance, _ := daemonIdleExportFixture(t)
	removeSyncLock(t, dir)
	runIdleDaemonTicks(t, dir, 10, advance, func(int) {})
	requireSyncLockUntouched(t, dir)
}

// The lab shape: the wake scanner is wired, a registered session is dead with
// unread mail, and idle waiters are attached. The scanner's per-pass writes
// (stale mark, scan cursor, leases) must not record an export request either.
func TestDaemon_IdleTicksWithWakeScannerAndDeadSessionRecordNoPendingExportRequest(t *testing.T) {
	initTestApp(t)
	dir := app.mtixDir
	hostID, uid, err := registrationHostIdentity()
	require.NoError(t, err)
	seedWakeScannerBoard(t, hostID, uid)
	bookkeeping := seedIdleBookkeeping(t, hostID, uid)
	wireWakeScanner(dir)
	require.NotNil(t, app.wakeScanner, "the scanner must be wired for this shape")
	for _, agent := range []string{"waiter-a", "waiter-b", "waiter-c"} {
		t.Cleanup(app.store.RegisterInboxWaiter(t.Context(), agent))
	}
	require.NoError(t, app.syncSvc.AutoExport(t.Context(), dir))
	removeSyncLock(t, dir)
	runIdleDaemonTicks(t, dir, 5, func() {}, func(tick int) { bookkeeping.afterTick(t, tick) })
	var stale string
	require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT COALESCE(stale_since,'') FROM agent_registrations WHERE agent_id=?", "dead-agent").Scan(&stale))
	require.NotEmpty(t, stale, "the scanner must have run its per-pass write on the dead session")
	requireIdleBookkeepingPersisted(t)
	requireSyncLockUntouched(t, dir)
}

// A real exported-content write under the running daemon still records
// exactly one pending export request (the daemon only stops creating its own).
func TestDaemon_RealWriteRecordsExactlyOnePendingExportRequest(t *testing.T) {
	dir, advance, _ := daemonIdleExportFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
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
	go func() { done <- runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, control) }()
	t.Cleanup(func() { cancel(); awaitDispatchDone(t, done) })
	require.True(t, awaitDispatchObservation(t, observed))
	advance()
	title := "changed"
	// The debouncer waits its quiet period, so the request is still observable.
	require.NoError(t, app.store.UpdateNode(t.Context(), "TEST-1", &store.NodeUpdate{Title: &title}))
	status, err := app.syncSvc.PendingExports(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, 1, status.Count)
}

// runIdleDaemonTicks runs the real daemon for n controlled ticks and asserts
// no pending export request exists after the start and after every tick.
func runIdleDaemonTicks(t *testing.T, dir string, n int, advance func(), each func(tick int)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
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
	go func() { done <- runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, control) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			awaitDispatchDone(t, done)
		}
	})
	require.True(t, awaitDispatchObservation(t, observed))
	requireNoPendingExport(t, dir, 0)
	for tick := 1; tick <= n; tick++ {
		advance()
		sendDispatchTick(t, ticks, done)
		require.True(t, awaitDispatchObservation(t, observed))
		requireNoPendingExport(t, dir, tick)
		each(tick)
	}
	cancel()
	awaitDispatchDone(t, done)
	joined = true
}

func requireNoPendingExport(t *testing.T, dir string, tick int) {
	t.Helper()
	status, err := app.syncSvc.PendingExports(t.Context(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count, "idle daemon tick %d recorded a pending export request", tick)
}

// removeSyncLock deletes the lock file so a later check can tell whether any
// export pass opened it (every pass opens it before trying the flock).
func removeSyncLock(t *testing.T, dir string) {
	t.Helper()
	err := os.Remove(filepath.Join(dir, "data", "sync.lock"))
	require.True(t, err == nil || os.IsNotExist(err), "remove sync.lock: %v", err)
}

func requireSyncLockUntouched(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "data", "sync.lock"))
	require.True(t, os.IsNotExist(err), "an idle tick opened sync.lock (an export pass ran): %v", err)
}
