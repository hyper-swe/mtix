// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type pendingExportHandler struct{ callback func() }

func (pendingExportHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h pendingExportHandler) WithAttrs([]slog.Attr) slog.Handler     { return h }
func (h pendingExportHandler) WithGroup(string) slog.Handler          { return h }
func (h pendingExportHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "sync_export_completed" {
		h.callback()
	}
	return nil
}

func TestPendingExport_LateRequestRetainedThenDrained(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.NoError(t, markPendingExport(dir))
	called := false
	svc.logger = slog.New(pendingExportHandler{callback: func() {
		if called {
			return
		}
		called = true
		pendingProcessNode(t, store, 2)
		require.NoError(t, markPendingExport(dir))
	}})
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, requests, 1, "a request created after the snapshot must survive retirement")
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir))
	requests, err = pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
}

func TestPendingExport_PublicationFailureRetainsRequest(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	hash := filepath.Join(dir, "data", "sync-db.sha256")
	require.NoError(t, os.Mkdir(hash, 0755))
	require.ErrorContains(t, svc.AutoExport(context.Background(), dir), "write db hash")
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.NoError(t, os.Remove(hash))
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	requests, err = pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
}

func TestPendingExport_RefusedBoardPreserved(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	path := filepath.Join(dir, "tasks.json")
	board := []byte("{invalid pulled board}")
	require.NoError(t, os.WriteFile(path, board, 0644))
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, board, actual)
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.NotNil(t, svc.readRefusal(dir))
}

type pendingImportOff struct{}

func (pendingImportOff) AutoSyncSetting() (string, bool, error) { return "false", false, nil }

func TestPendingExport_StartupWithAutoImportOffRecovers(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	pendingProcessNode(t, store, 2)
	require.NoError(t, markPendingExport(dir))
	restarted := NewSyncService(store, slog.Default(), pendingProcessClock)
	restarted.SetAutoSyncConfig(pendingImportOff{})
	require.NoError(t, restarted.AutoImport(context.Background(), dir))
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir))
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
}

func TestPendingExport_NextAutoExportCoalescesRequests(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, markPendingExport(dir))
	require.NoError(t, markPendingExport(dir))
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests, "normal success must leave no token accumulation")
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
}

func TestPendingExport_UnderLockGuardPreservesChangedBoard(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	path := filepath.Join(dir, "tasks.json")
	board := []byte("{pulled after initial guard}")
	require.NoError(t, os.WriteFile(path, board, 0644))
	require.NoError(t, markPendingExport(dir))
	require.NoError(t, svc.exportBoardProtected(context.Background(), dir, true))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, board, actual)
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, requests, 1)
}
