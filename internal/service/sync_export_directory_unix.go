// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

func syncExportDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open export directory for sync: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return exportDirectorySyncResult(path, syncErr, closeErr)
}

// Only unsupported directory Sync is best-effort. Open and Close errors, and
// all file Sync errors, remain fatal; directory power-loss durability varies.
func exportDirectorySyncResult(path string, syncErr, closeErr error) error {
	if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
		slog.Warn("export directory sync unsupported; directory durability is best-effort", "path", path, "error", syncErr)
		syncErr = nil
	}
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist export directory: %w", err)
	}
	return nil
}
