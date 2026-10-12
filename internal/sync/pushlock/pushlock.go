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
	"strings"
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
	return AcquireNamed(mtixDir, pushLockFilename)
}

// AcquireNamed takes a nonblocking kernel lock at mtixDir/data/name per
// FR-18.18 and FR-20. The opened directory and regular lock file must belong
// to the effective user; symlinks and Windows reparse points are refused.
// Names are single portable filenames. ErrLockHeld denotes normal contention.
func AcquireNamed(mtixDir, name string) (*PushLock, error) {
	if mtixDir == "" {
		return nil, fmt.Errorf("acquire push lock: mtixDir required")
	}
	if err := validateLockName(name); err != nil {
		return nil, err
	}
	dir := filepath.Join(mtixDir, pushLockSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := platformOpenLock(dir, name)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := platformAcquire(f); err != nil {
		closeErr := f.Close()
		if errors.Is(err, errPlatformLockHeld) {
			err = ErrLockHeld
		}
		return nil, fmt.Errorf("acquire named lock: %w", errors.Join(err, closeErr))
	}
	return &PushLock{f: f}, nil
}

func validateLockName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.HasSuffix(name, ".") {
		return fmt.Errorf("acquire named lock: invalid filename")
	}
	for _, ch := range name {
		if !portableLockCharacter(ch) {
			return fmt.Errorf("acquire named lock: invalid filename")
		}
	}
	if reservedLockName(name) {
		return fmt.Errorf("acquire named lock: invalid filename")
	}

	return nil
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

func portableLockCharacter(ch rune) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-'
}
func reservedLockName(name string) bool {
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9'
}
