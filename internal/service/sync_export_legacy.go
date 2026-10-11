// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

type legacyExportRequest struct {
	path string
	info os.FileInfo
}

func legacyExportRequests(mtixDir string) ([]legacyExportRequest, error) {
	paths, err := pendingExportRequests(mtixDir)
	if err != nil {
		return nil, err
	}
	var requests []legacyExportRequest
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat legacy export request: %w", err)
		}
		if info.Mode().IsRegular() {
			requests = append(requests, legacyExportRequest{path, info})
		}
	}
	return requests, nil
}

func oldestLegacyRequest(requests []legacyExportRequest) time.Time {
	var oldest time.Time
	for _, request := range requests {
		if oldest.IsZero() || request.info.ModTime().Before(oldest) {
			oldest = request.info.ModTime()
		}
	}
	return oldest
}

func (s *SyncService) migratePendingExports(ctx context.Context, mtixDir string) error {
	requests, err := legacyExportRequests(mtixDir)
	if err != nil || len(requests) == 0 {
		return err
	}
	// Commit durable coalesced intent BEFORE deleting even one legacy token.
	if err := s.store.MarkExportRequest(ctx, oldestLegacyRequest(requests)); err != nil {
		return fmt.Errorf("migrate legacy export requests: %w", err)
	}
	if err := removeLegacyExportRequests(requests); err != nil {
		return err
	}
	return s.syncDirectory(pendingExportDir(mtixDir))
}

func removeLegacyExportRequests(requests []legacyExportRequest) error {
	for _, request := range requests {
		current, err := os.Lstat(request.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("recheck legacy export request: %w", err)
		}
		if !os.SameFile(request.info, current) || current.Mode() != request.info.Mode() ||
			current.Size() != request.info.Size() || !current.ModTime().Equal(request.info.ModTime()) {
			continue // A replacement is a new, uncovered request, even under the same name.
		}
		// Uncoordinated old writers can still replace between recheck and unlink;
		// this detects observed replacements, not an atomic filesystem CAS.
		if err := os.Remove(request.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove migrated legacy export request: %w", err)
		}
	}
	return nil
}
