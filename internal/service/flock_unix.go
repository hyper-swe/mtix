// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// fileFd returns the file descriptor as an int, safe for use with syscall.Flock.
// On all supported platforms, file descriptors fit in int. The explicit
// bounds check satisfies gosec G115 (integer overflow conversion).
func fileFd(f *os.File) int {
	fd := f.Fd()
	// File descriptors are small non-negative integers on all Unix systems.
	// This check satisfies the static analyzer while being effectively a no-op.
	if fd > uintptr(^uint(0)>>1) {
		return -1
	}
	return int(fd) //nolint:gosec // bounds-checked above
}

// acquireLock acquires a file lock on .mtix/data/sync.lock per FR-15.8.
// lockType is syscall.LOCK_SH for shared (import) or syscall.LOCK_EX for
// exclusive (export). Returns the lock file to be closed by the caller.
func (s *SyncService) acquireLock(mtixDir string, lockType int) (*os.File, error) {
	lockPath := filepath.Join(mtixDir, "data", "sync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// Non-blocking attempt first, then fail if locked.
	if flockErr := syscall.Flock(fileFd(f), lockType|syscall.LOCK_NB); flockErr != nil {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Error("failed to close lock file after flock failure", "error", closeErr)
		}
		return nil, fmt.Errorf("acquire sync lock: %w", flockErr)
	}

	return f, nil
}

// releaseLock releases the file lock per FR-15.8.
func (s *SyncService) releaseLock(f *os.File) {
	if f == nil {
		return
	}
	if err := syscall.Flock(fileFd(f), syscall.LOCK_UN); err != nil {
		s.logger.Error("failed to release sync lock", "error", err)
	}
	if err := f.Close(); err != nil {
		s.logger.Error("failed to close lock file", "error", err)
	}
}

// lockShared is the lock type for shared (read) access.
const lockShared = syscall.LOCK_SH

// lockExclusive is the lock type for exclusive (write) access.
const lockExclusive = syscall.LOCK_EX
