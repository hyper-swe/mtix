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

func TestRenameNoReplace_Cases(t *testing.T) {
	for _, tt := range []struct {
		name string
		move func(oldPath, newPath string) error
	}{
		{"platform primitive", renameNoReplace},
		{"portable fallback", renameThenRecheck},
	} {
		t.Run(tt.name+"/target absent moves a directory", func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "lock"), filepath.Join(dir, "lock.aside")
			require.NoError(t, os.Mkdir(src, 0o700))
			require.NoError(t, tt.move(src, dst))
			_, err := os.Lstat(dst)
			assert.NoError(t, err)
			_, err = os.Lstat(src)
			assert.True(t, os.IsNotExist(err))
		})
		t.Run(tt.name+"/target present is never replaced", func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "lock"), filepath.Join(dir, "lock.aside")
			require.NoError(t, os.Mkdir(src, 0o700))
			require.NoError(t, os.WriteFile(dst, []byte("keep"), 0o600))
			err := tt.move(src, dst)
			require.ErrorIs(t, err, model.ErrAlreadyExists)
			body, readErr := os.ReadFile(dst)
			require.NoError(t, readErr)
			assert.Equal(t, "keep", string(body))
			_, err = os.Lstat(src)
			assert.NoError(t, err, "the source stays where it was")
		})
	}
}

// A regular file that appears at the lock path (a live enrolment took the lock
// after the type check) is a held lock: the move must put it back.
func TestMoveEnrolLockAside_RegularFileMoved_IsPutBack(t *testing.T) {
	dir := t.TempDir()
	lock, aside := filepath.Join(dir, "enroll.lock"), filepath.Join(dir, "enroll.lock.unprobeable")
	require.NoError(t, os.WriteFile(lock, []byte("held"), 0o600))
	err := moveEnrolLockAside(lock, aside)
	require.ErrorIs(t, err, model.ErrConflict)
	body, readErr := os.ReadFile(lock)
	require.NoError(t, readErr, "a regular lock must be back in place")
	assert.Equal(t, "held", string(body))
	_, statErr := os.Lstat(aside)
	assert.True(t, os.IsNotExist(statErr))
}
