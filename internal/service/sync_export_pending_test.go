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

func TestPendingExport_UnreadableExistingBoardPreserved(t *testing.T) {
	for _, trigger := range []string{"startup-import", "daemon-drain", "under-lock"} {
		t.Run(trigger, func(t *testing.T) { testPendingUnreadableBoard(t, trigger) })
	}
}

func testPendingUnreadableBoard(t *testing.T, trigger string) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	path := filepath.Join(dir, "tasks.json")
	pulled := []byte("{teammate board that must never be overwritten unread}")
	require.NoError(t, os.WriteFile(path, pulled, 0644))
	require.NoError(t, markPendingExport(dir))
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0644)) })
	_, err := os.ReadFile(path)
	require.ErrorIs(t, err, os.ErrPermission, "fixture must deny reads while allowing parent-directory rename")
	var recoveryErr error
	switch trigger {
	case "startup-import":
		recoveryErr = svc.AutoImport(context.Background(), dir)
	case "daemon-drain":
		recoveryErr = svc.DrainPendingExport(context.Background(), dir)
	case "under-lock":
		recoveryErr = svc.exportBoardProtected(context.Background(), dir, true)
	default:
		t.Fatalf("unknown recovery trigger %s", trigger)
	}
	require.NoError(t, os.Chmod(path, 0644))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, pulled, actual, "automatic recovery must preserve an existing unreadable board")
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, requests, 1, "unreadable board must retain its durable recovery request")
	require.ErrorIs(t, recoveryErr, os.ErrPermission, "unreadable-board recovery reports its read error")
}

func TestPendingExport_TrulyAbsentBoardCreated(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, markPendingExport(dir))
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
}

// A request-metadata error must stay visible without preventing an otherwise
// safe mirror publication. A regular file at the request directory makes both
// request creation and enumeration fail, while the board parent stays writable.
func TestPendingExport_MetadataFailureStillPublishesMirror(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
	pendingPath := pendingExportDir(dir)
	require.NoError(t, os.Remove(pendingPath))
	metadata := []byte("owned unavailable request metadata")
	require.NoError(t, os.WriteFile(pendingPath, metadata, 0644))
	_, err = os.ReadDir(pendingPath)
	require.Error(t, err, "fixture must reject request enumeration")
	pendingProcessNode(t, store, 2)
	committed, err := store.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.Equal(t, 2, committed.NodeCount, "the writer committed its task")

	exportErr := svc.AutoExport(context.Background(), dir)
	require.ErrorContains(t, exportErr, "pending export", "request metadata failure must remain visible")
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir),
		"failed request metadata must not suppress protected mirror publication")
	actual, err := os.ReadFile(pendingPath)
	require.NoError(t, err)
	require.Equal(t, metadata, actual, "unknown request metadata must be preserved")
}
