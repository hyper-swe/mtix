// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package pushlock

import (
	"errors"
	"os"
	"syscall"
)

// errPlatformLockHeld is the platform-specific sentinel returned when
// the flock attempt fails because another process holds the lock.
// pushlock.Acquire translates this into the public ErrLockHeld.
var errPlatformLockHeld = errors.New("platform: lock held")

// platformAcquire attempts a non-blocking exclusive flock on f.
// EWOULDBLOCK / EAGAIN both signal contention; any other syscall error
// is propagated verbatim (caller wraps with context).
func platformAcquire(f *os.File) error {
	if err := syscall.Flock(fdInt(f), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errPlatformLockHeld
		}
		return err
	}
	return nil
}

// platformRelease unlocks the flock. Closing the fd would also release
// the lock (kernel guarantee), but explicit unlock keeps the intent
// visible and short-circuits on any unlock error before close runs.
func platformRelease(f *os.File) error {
	return syscall.Flock(fdInt(f), syscall.LOCK_UN)
}

// fdInt narrows the file descriptor to int with bounds protection.
// On all supported Unix systems file descriptors fit in int comfortably;
// the bounds check is a defensive nicety for static analyzers.
func fdInt(f *os.File) int {
	fd := f.Fd()
	if fd > uintptr(^uint(0)>>1) {
		return -1
	}
	return int(fd) //nolint:gosec // bounds-checked above
}
