// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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
		require.NoError(t, syncExportDirectory("/dev/null"))
	} else {
		require.ErrorIs(t, syncExportDirectory("/dev/null"), errno, "other native device errors remain fatal")
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
				return exportDirectorySyncResult(path, errno, nil)
			}
			require.NoError(t, svc.AutoExport(context.Background(), dir))
			require.GreaterOrEqual(t, calls, 6, "mark, publication and retirement must use directory policy")
			require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
			requests, err := pendingExportRequests(dir)
			require.NoError(t, err)
			require.Empty(t, requests, "unsupported directory Sync must not prevent retirement")
		})
	}
}
func TestPendingExport_DirectoryPolicyPreservesOtherErrors(t *testing.T) {
	require.ErrorIs(t, exportDirectorySyncResult("owned", syscall.EIO, nil), syscall.EIO)
	require.ErrorIs(t, exportDirectorySyncResult("owned", syscall.EINVAL, syscall.EIO), syscall.EIO,
		"ignored Sync must not hide a Close error")
	require.ErrorIs(t, syncExportDirectory(filepath.Join(t.TempDir(), "absent")), os.ErrNotExist,
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
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Len(t, requests, 1, "failed publication durability must retain its request")
	svc.directorySync = nil
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
	requests, err = pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
}
