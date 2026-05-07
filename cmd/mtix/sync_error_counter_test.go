// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoteSyncResult_FailureBumpsCounter verifies that a failed
// push/pull increments meta.sync.consecutive_errors. The counter
// drives the mtix_sync_workflow MCP tool's hub-unreachable
// detection; tripping at >=3 is the hub-unreachable threshold.
func TestNoteSyncResult_FailureBumpsCounter(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	noteSyncResult(ctx, app.store, false)
	noteSyncResult(ctx, app.store, false)

	var v int
	err := app.store.QueryRow(ctx,
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'meta.sync.consecutive_errors'`,
	).Scan(&v)
	require.NoError(t, err)
	require.Equal(t, 2, v)
}

// TestNoteSyncResult_SuccessClearsCounter verifies that a successful
// push/pull resets the counter to 0 even if errors had accumulated.
func TestNoteSyncResult_SuccessClearsCounter(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	noteSyncResult(ctx, app.store, false)
	noteSyncResult(ctx, app.store, false)
	noteSyncResult(ctx, app.store, false)
	noteSyncResult(ctx, app.store, true)

	var v int
	err := app.store.QueryRow(ctx,
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'meta.sync.consecutive_errors'`,
	).Scan(&v)
	require.NoError(t, err)
	require.Equal(t, 0, v)
}

// TestNoteSyncResult_NilStoreNoPanic guards the helper against being
// invoked before the local store is initialized.
func TestNoteSyncResult_NilStoreNoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		noteSyncResult(context.Background(), nil, false)
		noteSyncResult(context.Background(), nil, true)
	})
}
