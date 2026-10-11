// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Receipt readers acquire existing ownership without creating operator files.
package service

import (
	"os"
	"path/filepath"
)

func (s *SyncService) openExchangeReadLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, "data", "sync.lock")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, exchangeUnavailable("lock-unavailable", nil)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := lockExchangeReader(f); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			s.logger.Warn("close receipt read lock", "error", closeErr)
		}
		return nil, err
	}
	return f, nil
}
