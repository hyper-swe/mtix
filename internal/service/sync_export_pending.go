// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func pendingExportDir(mtixDir string) string {
	return filepath.Join(mtixDir, "data", "export-pending")
}

// markPendingExport durably publishes a unique request before any guard or
// nonblocking lock attempt. A request conveys no authority to overwrite a
// pulled board. Empty O_EXCL files avoid read/modify/write races among writers.
func markPendingExport(mtixDir string) error {
	dir := pendingExportDir(mtixDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create pending export directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "request-")
	if err != nil {
		return fmt.Errorf("create pending export request: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist pending export request: %w", err)
	}
	if err := syncExportDirectory(dir); err != nil {
		return err
	}
	if err := syncExportDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	return syncExportDirectory(mtixDir)
}

func pendingExportRequests(mtixDir string) ([]string, error) {
	dir := pendingExportDir(mtixDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending export requests: %w", err)
	}
	var requests []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "request-") {
			requests = append(requests, filepath.Join(dir, entry.Name()))
		}
	}
	return requests, nil
}

// DrainPendingExport attempts one recovery pass without creating a request.
// Contention and pulled-board refusals keep existing requests intact. Callers
// may invoke this on daemon ticks or after releasing a service import lock.
func (s *SyncService) DrainPendingExport(ctx context.Context, mtixDir string) error {
	requests, err := pendingExportRequests(mtixDir)
	if err != nil || len(requests) == 0 {
		return err
	}
	return s.runAutoExport(ctx, mtixDir, false)
}

// retirePendingExports removes only the request snapshot taken before exporting
// the store. A writer arriving during publication remains for the next pass.
func retirePendingExports(mtixDir string, requests []string) error {
	for _, path := range requests {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("retire pending export request: %w", err)
		}
	}
	if len(requests) == 0 {
		return nil
	}
	return syncExportDirectory(pendingExportDir(mtixDir))
}

// Publication must reach the filesystem before retiring its durable requests.
// The DB baseline computation remains the existing separate snapshot (130.3).
func syncExportPublication(mtixDir string) error {
	for _, path := range []string{"tasks.json", "data/sync.sha256", "data/sync-db.sha256"} {
		file, err := os.OpenFile(filepath.Join(mtixDir, path), os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("open export for sync: %w", err)
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return fmt.Errorf("persist export: %w", err)
		}
	}
	if err := syncExportDirectory(filepath.Join(mtixDir, "data")); err != nil {
		return err
	}
	return syncExportDirectory(mtixDir)
}

// Public startup import drains even when automatic import is switched off.
// The internal import used by the export guard never re-enters recovery.
func (s *SyncService) drainAfterImport(ctx context.Context, mtixDir string) {
	if err := s.DrainPendingExport(ctx, mtixDir); err != nil {
		s.logger.Warn("pending auto-export failed after import", "error", err)
	}
}
