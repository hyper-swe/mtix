// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package pushlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errPlatformLockHeld is the platform-specific sentinel returned when
// the LockFileEx attempt fails because another process holds the lock.
// pushlock.Acquire translates this into the public ErrLockHeld.
var errPlatformLockHeld = errors.New("platform: lock held")

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// platformAcquire attempts a non-blocking exclusive lock via LockFileEx.
// ERROR_LOCK_VIOLATION signals that another process holds the lock; any
// other Windows error is propagated verbatim.
func platformAcquire(f *os.File) error {
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		handle,
		lockfileExclusiveLock|lockfileFailImmediately,
		0, 1, 0, &overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
			errors.Is(err, windows.ERROR_IO_PENDING) {
			return errPlatformLockHeld
		}
		return err
	}
	return nil
}

// platformRelease releases the LockFileEx-held byte range.
func platformRelease(f *os.File) error {
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
}
