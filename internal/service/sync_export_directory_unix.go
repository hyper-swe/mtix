// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
)

// unsupportedDirectorySyncWarned records directories already warned about, so
// the best-effort warning is emitted at most once per directory per process.
var unsupportedDirectorySyncWarned sync.Map

// unsupportedDirectorySyncAlreadyWarned records path and reports whether it
// was already recorded. Call it only when a warning will actually be logged.
func unsupportedDirectorySyncAlreadyWarned(path string) bool {
	_, warned := unsupportedDirectorySyncWarned.LoadOrStore(path, struct{}{})
	return warned
}

func syncExportDirectory(logger *slog.Logger, path string) error {
	// #nosec G703 -- production callers select local project directories or their fixed data/export-pending subdirectories for directory durability; received board contents do not select this path.
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open export directory for sync: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return exportDirectorySyncResult(logger, path, syncErr, closeErr)
}

// Only unsupported directory Sync is best-effort. Open and Close errors, and
// all file Sync errors, remain fatal; directory power-loss durability varies.
// The warning goes only to the caller's logger; a nil logger stays silent and
// does not consume the path's single warning.
func exportDirectorySyncResult(logger *slog.Logger, path string, syncErr, closeErr error) error {
	if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
		if logger != nil && !unsupportedDirectorySyncAlreadyWarned(path) {
			logger.Warn("export directory sync unsupported; directory durability is best-effort", "path", path, "error", syncErr)
		}
		syncErr = nil
	}
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist export directory: %w", err)
	}
	return nil
}
