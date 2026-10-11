// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingExports_LegacyMigrationRetainsReplacementAndLateToken(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, markPendingExport(dir))
	captured, err := legacyExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, captured, 1)
	require.NoError(t, store.MarkExportRequest(context.Background(), oldestLegacyRequest(captured)))
	// Crash after the durable migration commit may leave both representations.
	before, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 2, before.Count)
	replacement := filepath.Join(dir, "replacement")
	require.NoError(t, os.WriteFile(replacement, nil, 0644))
	require.NoError(t, os.Rename(replacement, captured[0].path))
	require.NoError(t, markPendingExport(dir))
	require.NoError(t, removeLegacyExportRequests(captured))
	remaining, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, remaining, 2, "same-name replacement and unknown late file are both uncovered")
	unrelated := filepath.Join(pendingExportDir(dir), "operator-note")
	require.NoError(t, os.WriteFile(unrelated, []byte("keep"), 0644))
	require.NoError(t, svc.migratePendingExports(context.Background(), dir))
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 1, status.Count)
	content, err := os.ReadFile(unrelated)
	require.NoError(t, err)
	require.Equal(t, []byte("keep"), content)
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	status, err = svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

func TestPendingExports_LegacyCommitFailureKeepsTokensAndPublishes(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, markPendingExport(dir))
	original, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.NoError(t, store.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "CREATE TRIGGER owned_pending_failure BEFORE INSERT ON meta WHEN NEW.key='meta.export.pending' BEGIN SELECT RAISE(ABORT,'owned migration failure'); END")
		return err
	}))
	require.ErrorContains(t, svc.AutoExport(context.Background(), dir), "owned migration failure")
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir), "metadata error must still permit protected native publication")
	remaining, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Equal(t, original, remaining, "migration cannot remove files before metadata is durable")
	require.NoError(t, store.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "DROP TRIGGER owned_pending_failure")
		return err
	}))
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

func TestPendingExports_StatusIsReadOnlyAndClampsFutureAge(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, store.MarkExportRequest(context.Background(), pendingProcessClock().Add(time.Hour)))
	require.NoError(t, markPendingExport(dir))
	before, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	files, err := pendingExportRequests(dir)
	require.NoError(t, err)
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 2, status.Count)
	require.Zero(t, status.OldestAgeSeconds)
	require.Zero(t, status.Age)
	after, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after)
	remaining, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Equal(t, files, remaining, "diagnostics do not migrate or delete legacy tokens")
}
