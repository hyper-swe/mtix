// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"
)

// PendingExportStatus describes logical coalesced intent plus unmigrated legacy
// tokens. Reading it never migrates, publishes, retires, or contacts a hub.
type PendingExportStatus struct {
	Count            int    `json:"count"`
	OldestAt         string `json:"oldest_at"`
	OldestAgeSeconds int64  `json:"oldest_age_seconds"`
	// Age retains subsecond precision for threshold comparisons, without
	// adding a second representation to the JSON diagnostics.
	Age time.Duration `json:"-"`
}

// PendingExports reports local pending work and its oldest age (MTIX-130.11).
func (s *SyncService) PendingExports(ctx context.Context, mtixDir string) (PendingExportStatus, error) {
	var status PendingExportStatus
	state, err := s.store.ReadExportRequest(ctx)
	if err != nil {
		return status, err
	}
	legacy, err := legacyExportRequests(mtixDir)
	if err != nil {
		return status, err
	}
	oldest := oldestLegacyRequest(legacy)
	status.Count = len(legacy)
	if state.Requested > state.Covered {
		status.Count++
		marked, err := time.Parse(time.RFC3339Nano, state.Oldest)
		if err != nil {
			return status, fmt.Errorf("parse oldest export request: %w", err)
		}
		if oldest.IsZero() || marked.Before(oldest) {
			oldest = marked
		}
	}
	if !oldest.IsZero() {
		status.OldestAt = oldest.UTC().Format(time.RFC3339Nano)
		age := s.clock().Sub(oldest)
		if age > 0 {
			status.Age = age
			status.OldestAgeSeconds = int64(age / time.Second)
		}
	}
	return status, nil
}
