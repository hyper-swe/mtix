// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func tornMarkerDir(t *testing.T) string {
	t.Helper()
	mtix := t.TempDir()
	require.NoError(t, os.MkdirAll(enrolDir(mtix), 0o700))
	require.NoError(t, os.WriteFile(enrolMarkerPath(mtix), []byte("{torn"), 0o600))
	return mtix
}

func unprobeableLockDir(t *testing.T) string {
	t.Helper()
	mtix := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(enrolDir(mtix), "data", enrolLockName), 0o700))
	return mtix
}

func TestCheckEnrolGuard_TornMarker_DistinctCauseAndDoctorProceeds(t *testing.T) {
	mtix := tornMarkerDir(t)
	err := CheckEnrolGuard(mtix, false)
	require.ErrorIs(t, err, ErrEnrolMarkerUnreadable)
	assert.NotErrorIs(t, err, ErrEnrolInterrupted)
	assert.NoError(t, CheckEnrolGuard(mtix, true), "top-level doctor must proceed so it can print the row")
}

func TestCheckEnrolGuard_UnprobeableLock_DistinctCauseAndDoctorProceeds(t *testing.T) {
	mtix := unprobeableLockDir(t)
	err := CheckEnrolGuard(mtix, false)
	require.ErrorIs(t, err, ErrEnrolLockUnprobeable)
	assert.NotErrorIs(t, err, ErrEnrolInProgress)
	assert.NoError(t, CheckEnrolGuard(mtix, true))
	assert.ErrorIs(t, ProbeEnrolLock(mtix), ErrEnrolLockUnprobeable)
}

func TestCheckEnrolGuard_HeldLock_StillRefusesDoctor(t *testing.T) {
	mtix := t.TempDir()
	lock, err := acquireEnrolLock(mtix)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	require.ErrorIs(t, CheckEnrolGuard(mtix, true), ErrEnrolInProgress)
}

func TestEnrolAsideDirs_ListsOnlyAsideStores(t *testing.T) {
	mtix := t.TempDir()
	for _, d := range []string{asideDirPrefix + "a", asideDirPrefix + "b", "data", "other"} {
		require.NoError(t, os.MkdirAll(filepath.Join(mtix, d), 0o700))
	}
	got, err := EnrolAsideDirs(mtix)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{filepath.Join(mtix, asideDirPrefix+"a"), filepath.Join(mtix, asideDirPrefix+"b")}, got)
}

func TestMoveAsideUnprobeableEnrolLock_Cases(t *testing.T) {
	t.Run("directory is moved aside not removed", func(t *testing.T) {
		mtix := unprobeableLockDir(t)
		require.NoError(t, MoveAsideUnprobeableEnrolLock(mtix))
		_, err := os.Lstat(EnrolLockPath(mtix) + ".unprobeable")
		assert.NoError(t, err)
		assert.NoError(t, ProbeEnrolLock(mtix))
	})
	t.Run("held lock is refused and untouched", func(t *testing.T) {
		mtix := t.TempDir()
		lock, err := acquireEnrolLock(mtix)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, lock.Release()) })
		require.ErrorIs(t, MoveAsideUnprobeableEnrolLock(mtix), ErrEnrolInProgress)
		_, statErr := os.Lstat(EnrolLockPath(mtix))
		assert.NoError(t, statErr)
	})
	t.Run("free regular lock needs no repair", func(t *testing.T) {
		mtix := t.TempDir()
		lock, err := acquireEnrolLock(mtix)
		require.NoError(t, err)
		require.NoError(t, lock.Release())
		require.NoError(t, MoveAsideUnprobeableEnrolLock(mtix))
		_, statErr := os.Lstat(EnrolLockPath(mtix))
		assert.NoError(t, statErr)
	})
	t.Run("existing aside target is not clobbered", func(t *testing.T) {
		mtix := unprobeableLockDir(t)
		require.NoError(t, os.MkdirAll(EnrolLockPath(mtix)+".unprobeable", 0o700))
		require.ErrorIs(t, MoveAsideUnprobeableEnrolLock(mtix), model.ErrAlreadyExists)
	})
	t.Run("absent lock is a no-op", func(t *testing.T) {
		assert.NoError(t, MoveAsideUnprobeableEnrolLock(t.TempDir()))
	})
	t.Run("regular unprobeable file is refused", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("skipped as root: a root process can open a mode-000 lock")
		}
		mtix := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Dir(EnrolLockPath(mtix)), 0o700))
		require.NoError(t, os.WriteFile(EnrolLockPath(mtix), nil, 0o600))
		require.NoError(t, os.Chmod(EnrolLockPath(mtix), 0o000))
		t.Cleanup(func() { _ = os.Chmod(EnrolLockPath(mtix), 0o600) })
		require.ErrorIs(t, MoveAsideUnprobeableEnrolLock(mtix), ErrEnrolLockUnprobeable)
		_, statErr := os.Lstat(EnrolLockPath(mtix))
		assert.NoError(t, statErr)
	})
}
