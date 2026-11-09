// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// /dev/null supplies a native unsupported Sync errno, not an unsupported
// directory filesystem. This existing helper test checks error classification.
func TestPendingExport_NativeDeviceSyncPolicy(t *testing.T) {
	file, err := os.Open("/dev/null")
	require.NoError(t, err)
	nativeErr := file.Sync()
	require.NoError(t, file.Close())
	var errno syscall.Errno
	require.True(t, errors.As(nativeErr, &errno))
	if errors.Is(nativeErr, syscall.EINVAL) || errors.Is(nativeErr, syscall.ENOTSUP) {
		require.NoError(t, syncExportDirectory(nil, "/dev/null"))
	} else {
		require.ErrorIs(t, syncExportDirectory(nil, "/dev/null"), errno, "other native device errors remain fatal")
	}
}
func TestPendingExport_MetadataPermissionFailureStillPublishes(t *testing.T) {
	for _, mode := range []os.FileMode{0555, 0} {
		t.Run(mode.String(), func(t *testing.T) { pendingMetadataPermission(t, mode) })
	}
}
func pendingMetadataPermission(t *testing.T, mode os.FileMode) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.NoError(t, markPendingExport(dir))
	original, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, original, 1)
	path := pendingExportDir(dir)
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0755)) })
	file, err := os.CreateTemp(path, "permission-proof-")
	if file != nil {
		require.NoError(t, file.Close())
	}
	require.ErrorIs(t, err, os.ErrPermission, "owned fixture must reject request creation")
	if mode == 0 {
		_, err = os.ReadDir(path)
		require.ErrorIs(t, err, os.ErrPermission)
	}
	pendingProcessNode(t, store, 2)
	exportErr := svc.AutoExport(context.Background(), dir)
	require.ErrorIs(t, exportErr, os.ErrPermission)
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir), "metadata permission error must not block publication")
	require.NoError(t, os.Chmod(path, 0755))
	if mode == 0 {
		requests, err := pendingExportRequests(dir)
		require.NoError(t, err)
		require.Equal(t, original, requests, "unreadable snapshot retains unknown requests")
	}
}

// Simulated unsupported directory Sync drives the full pipeline through the
// same classifier. Native request/publication file Sync and deletion stay real.
func TestPendingExport_UnsupportedDirectoryPipelineRetiresRequests(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.ENOTSUP} {
		t.Run(errno.Error(), func(t *testing.T) {
			dir := t.TempDir()
			store := pendingProcessStore(t, dir)
			pendingProcessNode(t, store, 1)
			svc := NewSyncService(store, slog.Default(), pendingProcessClock)
			calls := 0
			svc.directorySync = func(path string) error {
				calls++
				return exportDirectorySyncResult(svc.dirWarner, path, errno, nil)
			}
			require.NoError(t, svc.AutoExport(context.Background(), dir))
			require.GreaterOrEqual(t, calls, 2, "publication must use directory policy; coalesced mark/retirement use FULL WAL")
			require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
			requests, err := pendingExportRequests(dir)
			require.NoError(t, err)
			require.Empty(t, requests, "unsupported directory Sync must not prevent retirement")
		})
	}
}
func TestPendingExport_DirectoryPolicyPreservesOtherErrors(t *testing.T) {
	require.ErrorIs(t, exportDirectorySyncResult(nil, "owned", syscall.EIO, nil), syscall.EIO)
	require.ErrorIs(t, exportDirectorySyncResult(nil, "owned", syscall.EINVAL, syscall.EIO), syscall.EIO,
		"ignored Sync must not hide a Close error")
	require.ErrorIs(t, syncExportDirectory(nil, filepath.Join(t.TempDir(), "absent")), os.ErrNotExist,
		"Open failures remain errors")
}
func TestPendingExport_FailedDirectorySyncRetainsRequests(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	svc.directorySync = func(string) error { return syscall.EIO }
	require.ErrorIs(t, svc.AutoExport(context.Background(), dir), syscall.EIO)
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir), "mark failure must still attempt publication")
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 1, status.Count, "failed publication durability must retain coalesced intent")
	svc.directorySync = nil
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	status, err = svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

// The unsupported-directory warning uses the service's injected logger, never
// the global default, and fires at most once per directory per process.
func TestDirectorySync_EINVAL_WarnsOnceViaInjectedLogger(t *testing.T) {
	var global bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&global, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	var injected bytes.Buffer
	svc := NewSyncService(store, slog.New(slog.NewTextHandler(&injected, nil)), pendingProcessClock)
	calls := map[string]int{}
	svc.directorySync = func(path string) error {
		calls[path]++
		return exportDirectorySyncResult(svc.dirWarner, path, syscall.EINVAL, nil)
	}
	for i := 0; i < 3; i++ {
		pendingProcessNode(t, store, i+2)
		require.NoError(t, svc.AutoExport(context.Background(), dir))
	}
	require.Greater(t, calls[dir], 1, "repeated exports must sync the same directory more than once")
	const msg = "export directory sync unsupported"
	require.Equal(t, 1, strings.Count(injected.String(), "path="+dir+" "), "one warning for the project directory")
	require.Equal(t, len(calls), strings.Count(injected.String(), msg), "one warning per distinct directory")
	require.NotContains(t, global.String(), msg, "global default logger must not receive the warning")
}

// The production default path (no directorySync override) must warn through
// the service's own logger and never the global default (MTIX-130.14). The
// service-level directoryFileSync hook makes the directory handle's Sync
// report EINVAL, so the real default path (syncDirectory -> syncExportDirectoryWith
// -> exportDirectorySyncResult) runs with no package-level seam.
func TestDirectorySync_DefaultPathPassesServiceLogger(t *testing.T) {
	var global bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&global, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	var own bytes.Buffer
	svc := NewSyncService(store, slog.New(slog.NewTextHandler(&own, nil)), pendingProcessClock)
	require.Nil(t, svc.directorySync, "test must exercise the default path")
	svc.directoryFileSync = func(*os.File) error { return syscall.EINVAL }
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	const msg = "export directory sync unsupported"
	require.Contains(t, own.String(), msg, "default path must warn via the service logger")
	require.NotContains(t, global.String(), msg, "global default logger must not receive the warning")
}

// A nil-logger (silent) call must not consume a directory's single warning.
func TestDirectorySync_NilLoggerDoesNotConsumeWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "silent-then-logged")
	require.NoError(t, exportDirectorySyncResult(nil, path, syscall.EINVAL, nil))
	var out bytes.Buffer
	require.NoError(t, exportDirectorySyncResult(newDirectoryWarner(slog.New(slog.NewTextHandler(&out, nil))), path, syscall.EINVAL, nil))
	require.Contains(t, out.String(), "export directory sync unsupported")
}
