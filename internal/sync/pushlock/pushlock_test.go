// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package pushlock_test

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/stretchr/testify/require"
)

func TestPushLock_FirstAcquireSucceeds(t *testing.T) {
	dir := t.TempDir()
	l, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	require.NotNil(t, l)
	require.NoError(t, l.Release())
}

func TestPushLock_SecondConcurrentAcquireFailsWithErrLockHeld(t *testing.T) {
	dir := t.TempDir()

	first, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	second, err := pushlock.Acquire(dir)
	require.Nil(t, second)
	require.Error(t, err)
	require.True(t, errors.Is(err, pushlock.ErrLockHeld),
		"second acquire MUST return ErrLockHeld so callers can errors.Is")
}

func TestPushLock_AfterReleaseSecondAcquireSucceeds(t *testing.T) {
	dir := t.TempDir()

	first, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, first.Release())

	second, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, second.Release())
}

func TestPushLock_TenConcurrentAcquirers_ExactlyOneWins(t *testing.T) {
	dir := t.TempDir()

	const goroutines = 10
	var wonCount atomic.Int32
	var heldCount atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			l, err := pushlock.Acquire(dir)
			if err == nil {
				wonCount.Add(1)
				// Hold a beat so the others' Acquire calls observe contention.
				time.Sleep(50 * time.Millisecond)
				_ = l.Release()
				return
			}
			if errors.Is(err, pushlock.ErrLockHeld) {
				heldCount.Add(1)
				return
			}
			t.Errorf("unexpected error: %v", err)
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(1), wonCount.Load(),
		"exactly one goroutine MUST win the singleton lock")
	require.Equal(t, int32(goroutines-1), heldCount.Load(),
		"all other goroutines MUST observe ErrLockHeld")
}

func TestPushLock_ReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	l, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, l.Release())
	require.NoError(t, l.Release(), "second Release on the same lock is a no-op")
}

func TestPushLock_RequiresMtixDir(t *testing.T) {
	_, err := pushlock.Acquire("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mtixDir required")
}

func TestPushLock_AutoReleaseOnProcessExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subprocess auto-release verified separately on Windows in MTIX-15.9")
	}
	if os.Getenv("MTIX_PUSHLOCK_CHILD") != "" {
		// Child invocation: acquire and exit immediately. The kernel
		// auto-releases the flock on process exit per FR-18.18.
		dir := os.Getenv("MTIX_PUSHLOCK_DIR")
		_, err := pushlock.Acquire(dir)
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0],
		"-test.run=TestPushLock_AutoReleaseOnProcessExit",
		"-test.v=false",
		"-test.timeout=10s",
	)
	cmd.Env = append(os.Environ(),
		"MTIX_PUSHLOCK_CHILD=1",
		"MTIX_PUSHLOCK_DIR="+dir,
	)
	require.NoError(t, cmd.Run())

	// Parent immediately tries to acquire — the lock MUST be free
	// because the child exited.
	l, err := pushlock.Acquire(dir)
	require.NoError(t, err, "kernel-level auto-release should free the lock at child exit")
	require.NoError(t, l.Release())
}
