// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package testutil provides shared test helpers for mtix.
// All helpers use t.Helper() for correct line reporting per TDD-WORKFLOW.md §4.
package testutil

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// NewTestStore creates a fresh SQLite store backed by a temp-file database.
// The database uses a file (not :memory:) because the dual read/write connection
// pool requires a file-based DB per EXECUTION-PLAN.md §8.
// The temp directory is automatically cleaned up when the test completes.
func NewTestStore(t *testing.T) store.Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "mtix_test.db")
	logger := slog.Default()

	s, err := sqlite.New(dbPath, logger)
	if err != nil {
		t.Fatalf("NewTestStore: failed to create store: %v", err)
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("NewTestStore cleanup: failed to close store: %v", err)
		}
	})

	return s
}
