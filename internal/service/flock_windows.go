// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// acquireLock acquires a file lock on .mtix/data/sync.lock per FR-15.8.
// Windows uses a native nonblocking shared or exclusive byte-range lock.
func (s *SyncService) acquireLock(mtixDir string, lockType int) (*os.File, error) {
	lockPath := filepath.Join(mtixDir, "data", "sync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if lockType == lockExclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &windows.Overlapped{}); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Error("close sync lock", "error", closeErr)
		}
		return nil, fmt.Errorf("acquire sync lock: %w", err)
	}
	return f, nil
}

// releaseLock releases the file lock per FR-15.8.
func (s *SyncService) releaseLock(f *os.File) {
	if f == nil {
		return
	}
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{}); err != nil {
		s.logger.Error("unlock sync lock", "error", err)
	}
	if err := f.Close(); err != nil {
		s.logger.Error("failed to close lock file", "error", err)
	}
}

// lockShared is the lock type for shared (read) access.
const lockShared = 0

// lockExclusive is the lock type for exclusive (write) access.
const lockExclusive = 1
