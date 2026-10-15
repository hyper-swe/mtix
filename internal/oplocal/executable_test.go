// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

// Release-defined executables in the operator directory (MTIX-112.19.3, ADR-007 §6.5).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteExecutable_NewFile_Mode0700InOperatorDirectory(t *testing.T) {
	s := identityState(t)
	path, err := s.WriteExecutable("mtix-shim", []byte("#!/bin/sh\n"))
	require.NoError(t, err)
	dir, err := s.Path()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "mtix-shim"), path)
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	assert.True(t, info.Mode().IsRegular())

	data, readPath, err := s.ReadExecutable("mtix-shim")
	require.NoError(t, err)
	assert.Equal(t, path, readPath)
	assert.Equal(t, "#!/bin/sh\n", string(data))
}

func TestWriteExecutable_Replace_AtomicNewBytes(t *testing.T) {
	s := identityState(t)
	_, err := s.WriteExecutable("mtix-shim", []byte("old"))
	require.NoError(t, err)
	_, err = s.WriteExecutable("mtix-shim", []byte("new"))
	require.NoError(t, err)
	data, _, err := s.ReadExecutable("mtix-shim")
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
}

func TestWriteExecutable_InvalidNames_Refused(t *testing.T) {
	s := identityState(t)
	for _, name := range []string{"", "Shim", "shim.json", "../shim", "host-id", "a/b", ".state-x", "-shim"} {
		t.Run(name, func(t *testing.T) {
			_, err := s.WriteExecutable(name, []byte("x"))
			assert.Error(t, err)
			_, _, err = s.ReadExecutable(name)
			assert.Error(t, err)
		})
	}
}

func TestReadExecutable_Absent_NotExist(t *testing.T) {
	s := identityState(t)
	_, _, err := s.ReadExecutable("mtix-shim")
	assert.True(t, errors.Is(err, os.ErrNotExist), "%v", err)
	require.NoError(t, s.Ensure())
	_, _, err = s.ReadExecutable("mtix-shim")
	assert.True(t, errors.Is(err, os.ErrNotExist), "%v", err)
}

func TestReadExecutable_WrongModeOrSymlink_Refused(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{"group-readable", func(t *testing.T, path string) { require.NoError(t, os.Chmod(path, 0o750)) }},
		{"not executable", func(t *testing.T, path string) { require.NoError(t, os.Chmod(path, 0o600)) }},
		{"symbolic link", func(t *testing.T, path string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Symlink("/bin/sh", path))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := identityState(t)
			path, err := s.WriteExecutable("mtix-shim", []byte("x"))
			require.NoError(t, err)
			tt.prepare(t, path)
			_, _, err = s.ReadExecutable("mtix-shim")
			assert.Error(t, err)
			assert.False(t, errors.Is(err, os.ErrNotExist))
		})
	}
}

func TestWriteExecutable_RecordsUnaffected_InspectIgnoresExecutable(t *testing.T) {
	s := identityState(t)
	require.NoError(t, s.WriteJSON("grant-fixture", map[string]string{"a": "b"}))
	_, err := s.WriteExecutable("mtix-shim", []byte("x"))
	require.NoError(t, err)
	status, err := s.Inspect()
	require.NoError(t, err)
	assert.Equal(t, "ready", status.Status)
	assert.Empty(t, status.Reports)
}
