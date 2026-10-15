// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openAlertLock(dir *os.File, create bool) (*os.File, error) {
	disposition := uint32(windows.FILE_OPEN)
	if create {
		disposition = windows.FILE_OPEN_IF
	}
	f, err := relativeOpen(dir, "alerts.lock", windows.FILE_NON_DIRECTORY_FILE, disposition, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return nil, err
	}
	if err = verifyFile(f, false); err != nil {
		return nil, errors.Join(err, closeFile(f))
	}
	return f, nil
}
func tryAlertLock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}
func busyAlertLock(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
func unlockAlert(f *os.File) error {
	return errors.Join(windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{}), closeFile(f))
}
