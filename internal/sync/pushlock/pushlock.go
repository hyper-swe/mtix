// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package pushlock provides the singleton-pusher file lock primitive
// per FR-18.18 / SYNC-DESIGN section "D13. Singleton pusher via flock".
//
// The lock lives at .mtix/data/sync.push.lock and is acquired with an
// exclusive, non-blocking flock. When N concurrent agents on one machine
// emit mutations, exactly one process becomes the active pusher; others
// observe ErrLockHeld and exit. The lock is auto-released on process
// exit (kernel-level guarantee) so a crashed pusher never strands the
// queue.
//
// The actual push goroutine that uses this lock lands in MTIX-15.7
// (CLI commands). This package only provides the primitive.
package pushlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrLockHeld is returned when another process already holds the
// singleton-pusher lock. Callers MUST treat this as a normal "someone
// else is pushing" signal — not a failure to propagate.
var ErrLockHeld = errors.New("sync push lock held by another process")

// PushLock is a held singleton-pusher lock. Callers Release() when done.
// On process exit the kernel auto-releases regardless.
type PushLock struct {
	f *os.File
}

// pushLockFilename is the filename under .mtix/data/.
const pushLockFilename = "sync.push.lock"

// pushLockSubdir is the subdirectory under the mtix dir where the lock
// lives. Mirrors FR-15.8 (sync_service uses .mtix/data/sync.lock).
const pushLockSubdir = "data"

// pushLockMode is the file permission mode used when creating the lock
// file. 0600 keeps it user-readable only — the lock file is local
// process coordination, never a credential.
const pushLockMode os.FileMode = 0o600

// Acquire attempts to take the exclusive, non-blocking push lock under
// mtixDir/data/sync.push.lock. Returns the held lock on success;
// ErrLockHeld if another process owns it.
//
// Implementations live in pushlock_unix.go and pushlock_windows.go.
func Acquire(mtixDir string) (*PushLock, error) {
	if mtixDir == "" {
		return nil, fmt.Errorf("acquire push lock: mtixDir required")
	}
	dir := filepath.Join(mtixDir, pushLockSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	path := filepath.Join(dir, pushLockFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, pushLockMode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := platformAcquire(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errPlatformLockHeld) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("acquire push lock: %w", err)
	}
	return &PushLock{f: f}, nil
}

// Release frees the lock. Idempotent — calling Release twice is a no-op.
// Returns the first error encountered (release or close).
func (l *PushLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	releaseErr := platformRelease(l.f)
	closeErr := l.f.Close()
	l.f = nil
	if releaseErr != nil {
		return fmt.Errorf("release push lock: %w", releaseErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close push lock: %w", closeErr)
	}
	return nil
}
