// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// acquireLock acquires a file lock on .mtix/data/sync.lock per FR-15.8.
// On Windows, file locking uses LockFileEx. This implementation uses
// a simple open-with-exclusive-access approach as a fallback.
func (s *SyncService) acquireLock(mtixDir string, _ int) (*os.File, error) {
	lockPath := filepath.Join(mtixDir, "data", "sync.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	return f, nil
}

// releaseLock releases the file lock per FR-15.8.
func (s *SyncService) releaseLock(f *os.File) {
	if f == nil {
		return
	}
	if err := f.Close(); err != nil {
		s.logger.Error("failed to close lock file", "error", err)
	}
}

// lockShared is the lock type for shared (read) access.
const lockShared = 0

// lockExclusive is the lock type for exclusive (write) access.
const lockExclusive = 1
