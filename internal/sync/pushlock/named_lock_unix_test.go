// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Unix named-lock controls test opened objects and synthetic metadata.
package pushlock

import (
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestNamedLock_UnixMetadata_ValidatesTypeAndOwner(t *testing.T) {
	cases := []struct {
		name      string
		owner     uint32
		mode      uint32
		directory bool
		wantErr   bool
	}{
		{"owned-dir", 123, unix.S_IFDIR | 0755, true, false},
		{"owned-file", 123, unix.S_IFREG | 0600, false, false},
		{"foreign-dir", 124, unix.S_IFDIR | 0755, true, true},
		{"foreign-file", 124, unix.S_IFREG | 0600, false, true},
		{"wrong-dir-type", 123, unix.S_IFREG | 0600, true, true},
		{"wrong-file-type", 123, unix.S_IFIFO | 0600, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// SYNTHETIC owner/mode rows; not a privileged chown execution claim.
			err := validateLockMetadata(tc.owner, tc.mode, 123, tc.directory)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	require.Error(t, validateLockMetadata(0, unix.S_IFREG, -1, false))
}

func TestNamedLock_UnixFIFO_RefusedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "data"), 0755))
	require.NoError(t, unix.Mkfifo(filepath.Join(dir, "data", "dispatch.lock"), 0600))
	lock, err := AcquireNamed(dir, "dispatch.lock")
	require.Error(t, err)
	require.Nil(t, lock)
}

func TestNamedLock_UnixExclusiveCreateAndExistingOpen_SameObject(t *testing.T) {
	dir := t.TempDir()
	parent, err := os.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, parent.Close()) })
	for _, phase := range []string{"exclusive-create", "existing-open"} {
		t.Run(phase, func(t *testing.T) {
			fd, err := openNamedLockAt(fdInt(parent), "dispatch.lock")
			require.NoError(t, err)
			file, err := lockDescriptorFile(fd, "dispatch.lock")
			require.NoError(t, err)
			defer func() { require.NoError(t, file.Close()) }()
			require.NoError(t, verifyLockObject(file, false))
			opened, err := file.Stat()
			require.NoError(t, err)
			onDisk, err := os.Stat(filepath.Join(dir, "dispatch.lock"))
			require.NoError(t, err)
			require.True(t, os.SameFile(opened, onDisk), "create and existing reopen retain the real named object")
		})
	}
	fd, err := openNamedLockAt(fdInt(parent), "missing/dispatch.lock")
	require.ErrorIs(t, err, unix.ENOENT)
	require.Equal(t, -1, fd, "absent parent is reported without fallback or ENOENT retry")
}
