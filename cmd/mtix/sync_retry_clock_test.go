// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Native ownership stays real. Only elapsed retry time advances without sleeps.
func holdFastCLIImportLock(t *testing.T) *os.File {
	t.Helper()
	lock := holdCLIImportLock(t)
	useFastCLIImportClock(t)
	return lock
}

func useFastCLIImportClock(t *testing.T) {
	t.Helper()
	app.syncOptions = syncServiceOptions{retryClock: advancingCLIImportClock(nil)}
}

func advancingCLIImportClock(onTick func(int64)) func() time.Time {
	base := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	var ticks atomic.Int64
	return func() time.Time {
		tick := ticks.Add(1)
		if onTick != nil {
			onTick(tick)
		}
		return base.Add(time.Duration(tick) * time.Second)
	}
}

func requireCLIImportLockStillHeld(t *testing.T) {
	t.Helper()
	probe, err := os.OpenFile(filepath.Join(".mtix", "data", "sync.lock"), os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer func() { require.NoError(t, probe.Close()) }()
	err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	require.ErrorIs(t, err, syscall.EWOULDBLOCK, "a distinct descriptor must still see real native ownership")
}

func TestCLIImportRetry_ConstructorClockExpiresRealContentionWithoutWallWait(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	holdCLIImportLock(t)
	var expiredWhileHeld atomic.Bool
	var ticks atomic.Int64
	base := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	app.syncOptions = syncServiceOptions{retryClock: func() time.Time {
		tick := ticks.Add(1)
		if tick == 2 {
			// Retry start, then the failed native attempt's elapsed
			// time cap: the lock is still held when the cap expires.
			requireCLIImportLockStillHeld(t)
			expiredWhileHeld.Store(true)
		}
		return base.Add(time.Duration(tick) * time.Second)
	}}
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"show"})
	require.NoError(t, err)
	cmd.SetContext(testContext(t))
	require.NoError(t, initApp(cmd, ""))
	requireCLIImportLockStillHeld(t)
	before, err := os.ReadFile(filepath.Join(".mtix", "tasks.json"))
	require.NoError(t, err)
	hashBefore, err := os.ReadFile(filepath.Join(".mtix", "data", "sync.sha256"))
	require.NoError(t, err)
	ticks.Store(0)
	expiredWhileHeld.Store(false)
	started := time.Now()
	stderr := captureStderr(t, func() { autoImportForCommand(cmd) })
	elapsed := time.Since(started)
	// Only the ownership check is timed; SQLite/fixture setup is outside.
	// Ignoring the constructor option spends the actual 500 ms budget.
	require.Less(t, elapsed, 250*time.Millisecond, "constructor clock must eliminate the real retry wait")
	require.True(t, expiredWhileHeld.Load(), "the injected clock must expire while native ownership is held")
	requireCLIImportLockStillHeld(t)
	require.Equal(t, "sync_lock_busy", app.staleReason)
	require.Contains(t, stderr, "possibly stale")
	_, err = app.store.GetNode(testContext(t), "TEST-2")
	require.ErrorIs(t, err, model.ErrNotFound, "contention must not import the teammate task")
	after, err := os.ReadFile(filepath.Join(".mtix", "tasks.json"))
	require.NoError(t, err)
	require.Equal(t, before, after, "contention must preserve the pulled board")
	hashAfter, err := os.ReadFile(filepath.Join(".mtix", "data", "sync.sha256"))
	require.NoError(t, err)
	require.Equal(t, hashBefore, hashAfter, "contention must not record a new imported hash")
}
