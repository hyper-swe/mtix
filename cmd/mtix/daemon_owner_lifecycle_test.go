// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDaemonOwner_ProcessCrashReleasesPersistentLock(t *testing.T) {
	for _, spelling := range []string{"daemon", "syncdaemon"} {
		t.Run(spelling, func(t *testing.T) { daemonOwnerCrashRecovery(t, spelling) })
	}
}
func daemonOwnerCrashRecovery(t *testing.T, spelling string) {
	dir := t.TempDir()
	crashed := daemonOwnerStart(t, dir, spelling)
	lockPath := filepath.Join(dir, "data", daemonLockFilename)
	original, err := os.Stat(lockPath)
	require.NoError(t, err)
	oldDescriptor, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, oldDescriptor.Close()) })
	marker := daemonOwnerMarker(t, dir)
	require.NoError(t, crashed.cmd.Process.Kill())
	select {
	case <-crashed.done:
		require.Error(t, crashed.err, "crash bypassed graceful cleanup")
	case <-time.After(60 * time.Second):
		t.Fatal("killed owner did not join")
	}
	require.Equal(t, marker, daemonOwnerMarker(t, dir), "crash leaves diagnostic marker")
	nextSpelling := "daemon"
	if spelling == "daemon" {
		nextSpelling = "syncdaemon"
	}
	survivor := daemonOwnerStart(t, dir, nextSpelling)
	current, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(original, current), "crash recovery must keep lock inode")
	require.ErrorIs(t, platformAcquireDaemonLock(oldDescriptor), errDaemonLockHeld,
		"descriptor opened before crash must contend with new native owner")
	survivor.command(t, "release")
	survivor.event(t, 1, "idle")
	survivor.command(t, "quit")
	survivor.join(t)
	after, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(original, after), "normal release must not unlink lock inode")
	require.NoError(t, platformAcquireDaemonLock(oldDescriptor), "native exit frees retained descriptor's lock")
}
func TestDaemonOwner_PIDPublicationFailureReleasesLock(t *testing.T) {
	for _, spelling := range []string{"daemon", "syncdaemon"} {
		t.Run(spelling, func(t *testing.T) { daemonOwnerPublicationFailure(t, spelling) })
	}
}
func daemonOwnerPublicationFailure(t *testing.T, spelling string) {
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	saved := app
	t.Cleanup(func() { app = saved })
	app = appContext{mtixDir: dir, store: store, logger: slog.Default(),
		hooksDisp: service.NewHooksDispatcher(store, dir, slog.Default())}
	path := filepath.Join(dir, daemonPIDFilename)
	foreign := []byte("918273645")
	require.NoError(t, os.WriteFile(path, foreign, 0600))
	require.NoError(t, os.Mkdir(path+".tmp", 0700))
	err := runDaemonOwnerNative(context.Background(), io.Discard, io.Discard, spelling)
	require.ErrorContains(t, err, "publish daemon PID")
	require.NotErrorIs(t, err, errDaemonLockHeld)
	require.Equal(t, foreign, daemonOwnerMarker(t, dir), "failed publication preserves foreign marker")
	require.NoError(t, os.Remove(path+".tmp"))
	owner, err := acquireDaemonOwnership(dir)
	require.NoError(t, err, "startup failure must close acquired lock")
	require.NoError(t, owner.release())
	require.NoError(t, owner.release(), "repeated cleanup is harmless")
}
func TestDaemonOwner_ReleaseReadErrorStillClosesLock(t *testing.T) {
	dir := t.TempDir()
	owner, err := acquireDaemonOwnership(dir)
	require.NoError(t, err)
	path := filepath.Join(dir, daemonPIDFilename)
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	_, readErr := os.ReadFile(path)
	require.ErrorIs(t, readErr, os.ErrPermission, "owned fixture must deny marker read")
	require.ErrorIs(t, owner.release(), os.ErrPermission, "cleanup propagates marker IO error")
	require.NoError(t, os.Chmod(path, 0600))
	replacement, err := acquireDaemonOwnership(dir)
	require.NoError(t, err, "cleanup error must still release lifetime lock")
	require.NoError(t, owner.release(), "released old owner cannot delete replacement PID")
	require.FileExists(t, path)
	require.NoError(t, replacement.release())
}
func TestDaemonOwner_LockErrorsRemainErrors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data"), []byte("owned obstruction"), 0600))
	owner, err := acquireDaemonOwnership(dir)
	require.Error(t, err)
	require.NotErrorIs(t, err, errDaemonLockHeld)
	require.Nil(t, owner)
	require.NoError(t, owner.release())
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data", daemonLockFilename), 0700))
	owner, err = acquireDaemonOwnership(dir)
	require.Error(t, err)
	require.NotErrorIs(t, err, errDaemonLockHeld)
	require.Nil(t, owner)
}
