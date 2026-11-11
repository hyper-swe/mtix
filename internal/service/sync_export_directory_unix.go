// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// syncExportDirectory makes a directory entry durable using the platform
// policy. A nil warner stays silent (MTIX-130.14).
func syncExportDirectory(w *directoryWarner, path string) error {
	return syncExportDirectoryWith(w, path, nil)
}

// syncExportDirectoryWith is syncExportDirectory with an optional replacement
// for the directory handle's Sync call; nil uses (*os.File).Sync. The
// replacement is an instance-level hook so tests can force the unsupported
// branch on the default path (MTIX-130.14).
func syncExportDirectoryWith(w *directoryWarner, path string, syncFile func(*os.File) error) error {
	// #nosec G703 -- production callers select local project directories or their fixed data/export-pending subdirectories for directory durability; received board contents do not select this path.
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open export directory for sync: %w", err)
	}
	var syncErr error
	if syncFile != nil {
		syncErr = syncFile(dir)
	} else {
		syncErr = dir.Sync()
	}
	closeErr := dir.Close()
	return exportDirectorySyncResult(w, path, syncErr, closeErr)
}

// Only unsupported directory Sync is best-effort. Open and Close errors, and
// all file Sync errors, remain fatal; directory power-loss durability varies.
// The warning goes only to the caller's logger; a nil logger stays silent and
// does not consume the path's single warning.
func exportDirectorySyncResult(w *directoryWarner, path string, syncErr, closeErr error) error {
	if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
		if w.shouldWarn(path) {
			w.logger.Warn("export directory sync unsupported; directory durability is best-effort", "path", path, "error", syncErr)
		}
		syncErr = nil
	}
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist export directory: %w", err)
	}
	return nil
}
