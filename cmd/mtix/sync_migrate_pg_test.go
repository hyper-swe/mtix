// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// TestRunSyncMigrate_EndToEnd drives the full CLI path against a live PG:
// Phase 0 (local backfill) → Phase 1/1.5/2/3 (hub) → report. PG-gated;
// skips when MTIX_PG_TEST_DSN is unset. Covers the runSyncMigrate wrapper
// the unit tests cannot reach without a real DSN + store.
func TestRunSyncMigrate_EndToEnd(t *testing.T) {
	dsn := requireCmdPG(t)
	openCmdHub(t)  // fresh-migrates the hub schema
	initTestApp(t) // app.store + app.mtixDir

	t.Setenv("MTIX_SYNC_DSN", dsn)
	t.Setenv("MTIX_SYNC_HOOK", "")

	// Dry-run on a clean hub: every phase is read-only, no mutation.
	var stdout, stderr bytes.Buffer
	err := runSyncMigrate(context.Background(), &stdout, &stderr, nil,
		transport.Options{InsecureTLS: true}, "MTIX", false)
	require.NoError(t, err, "stderr: %s", stderr.String())
	require.Contains(t, stdout.String(), "DRY RUN")
	require.Contains(t, stdout.String(), "0-backfill")

	// Apply on a clean hub: gate is closed (no clients registered), so the
	// index is deferred and cutover is deferred — but the run succeeds.
	var ao, ae bytes.Buffer
	err = runSyncMigrate(context.Background(), &ao, &ae, nil,
		transport.Options{InsecureTLS: true}, "MTIX", true)
	require.NoError(t, err, "stderr: %s", ae.String())
	require.Contains(t, ao.String(), "APPLY")
}
