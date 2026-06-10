// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Integration test for FR-15.3 mirror parity (MTIX-26.1): in a
// long-running process (MCP server, serve) the tasks.json mirror must be
// written after mutations WITHOUT the process exiting — the store's
// on-commit hook drives a debounced AutoExport. This is the redundancy
// layer whose absence on the MCP path caused the 2026-05-19 data loss.
package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// parityTestLogger keeps mirror-parity test output quiet but visible on error.
func parityTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestMirrorParity_OnCommitExportsWithoutProcessExit(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	mtixDir := filepath.Join(dir, ".mtix")
	tasksPath := filepath.Join(mtixDir, "tasks.json")

	exporter := service.NewExportDebouncer(
		func(ctx context.Context) error { return svc.AutoExport(ctx, mtixDir) },
		parityTestLogger(),
		20*time.Millisecond, 200*time.Millisecond,
	)
	defer exporter.Close()
	store.SetOnCommit(exporter.Trigger)

	_, statErr := os.Stat(tasksPath)
	require.True(t, os.IsNotExist(statErr), "no mirror before any mutation")

	// A committed write transaction — what any MCP tool call or HTTP
	// mutation performs — must reach the mirror with the process alive.
	ctx := context.Background()
	err := store.WithTx(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO meta (key, value) VALUES ('parity_probe', '1')`)
		return execErr
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := os.Stat(tasksPath)
		return err == nil
	}, 3*time.Second, 10*time.Millisecond,
		"tasks.json must appear after a committed mutation, without process exit")

	raw, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload), "mirror must be valid JSON")
	assert.Contains(t, payload, "node_count")
}

func TestMirrorParity_RolledBackTxDoesNotExport(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	mtixDir := filepath.Join(dir, ".mtix")
	tasksPath := filepath.Join(mtixDir, "tasks.json")

	exporter := service.NewExportDebouncer(
		func(ctx context.Context) error { return svc.AutoExport(ctx, mtixDir) },
		parityTestLogger(),
		20*time.Millisecond, 200*time.Millisecond,
	)
	defer exporter.Close()
	store.SetOnCommit(exporter.Trigger)

	ctx := context.Background()
	failErr := assert.AnError
	err := store.WithTx(ctx, func(tx *sql.Tx) error { return failErr })
	require.ErrorIs(t, err, failErr)

	time.Sleep(300 * time.Millisecond)
	_, statErr := os.Stat(tasksPath)
	assert.True(t, os.IsNotExist(statErr),
		"a rolled-back transaction must not trigger the mirror")
}
