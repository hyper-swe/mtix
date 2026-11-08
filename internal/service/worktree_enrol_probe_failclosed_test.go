// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-112.17.16.21.2.4: the lock probe is the evidence doctor uses to decide
// whether an enrolment may be live, so it must fail closed: only an absent lock
// is free.
func TestProbeEnrolLock_LockDirectoryNotSearchable_IsUnprobeable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipped as root: root can search a mode-000 directory, so the lock never becomes unprobeable")
	}
	mtix := t.TempDir()
	lockDir := filepath.Join(enrolDir(mtix), "data")
	require.NoError(t, os.MkdirAll(lockDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(lockDir, enrolLockName), nil, 0o600))
	require.NoError(t, os.Chmod(lockDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(lockDir, 0o700) })

	err := ProbeEnrolLock(mtix)
	require.ErrorIs(t, err, ErrEnrolLockUnprobeable, "a lock that cannot be inspected must not read as free")
	assert.NotErrorIs(t, err, ErrEnrolInProgress)
}

func TestProbeEnrolLock_AbsentLock_IsFree(t *testing.T) {
	assert.NoError(t, ProbeEnrolLock(t.TempDir()))
	mtix := t.TempDir()
	require.NoError(t, os.MkdirAll(enrolDir(mtix), 0o700)) // data/ absent: still ENOENT
	assert.NoError(t, ProbeEnrolLock(mtix))
}

func TestReadEnrolMarker_DecodeFailureIsDistinctFromReadFailure(t *testing.T) {
	torn := tornMarkerDir(t)
	_, err := ReadEnrolMarker(torn)
	require.ErrorIs(t, err, ErrEnrolMarkerUnreadable)
	assert.ErrorIs(t, err, ErrEnrolMarkerUndecodable, "a content failure is a decode failure")

	if os.Geteuid() == 0 {
		t.Log("read-failure half skipped as root: root can read a mode-000 marker")
		return
	}
	unreadable := tornMarkerDir(t)
	require.NoError(t, os.Chmod(enrolMarkerPath(unreadable), 0o000))
	t.Cleanup(func() { _ = os.Chmod(enrolMarkerPath(unreadable), 0o600) })
	_, err = ReadEnrolMarker(unreadable)
	require.ErrorIs(t, err, ErrEnrolMarkerUnreadable, "the API keeps one sentinel for both")
	assert.NotErrorIs(t, err, ErrEnrolMarkerUndecodable, "a permission failure is not a decode failure")
}
