// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Unix named-lock controls test opened objects and synthetic metadata.
package pushlock

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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

func TestNamedLock_UnixClosedMetadataAndInvalidDescriptor_Refused(t *testing.T) {
	file, err := os.Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.ErrorIs(t, verifyLockObject(file, true), unix.EBADF, "actual Fstat on a closed handle preserves the kernel error")
	invalid, err := lockDescriptorFile(-1, "invalid")
	require.Error(t, err)
	require.Nil(t, invalid)
	require.Contains(t, err.Error(), "descriptor is invalid")
}

func TestNamedLock_UnixForeignDirectory_Refused(t *testing.T) {
	var root unix.Stat_t
	require.NoError(t, unix.Stat("/", &root))
	if os.Geteuid() == 0 {
		t.Skip("privileged effective root owns the root directory; actual foreign-owner fixture unavailable")
	}
	require.NotEqual(t, uint32(os.Geteuid()), root.Uid, "actual OS directory owner must differ before claiming foreign-owner execution")
	lock, err := platformOpenLock("/", "dispatch.lock")
	require.Error(t, err)
	require.Nil(t, lock)
	require.Contains(t, err.Error(), "effective user ownership", "refusal is validated ownership, not a later create permission error")
	t.Logf("actual foreign-owned root directory checked read-only: euid=%d owner=%d", os.Geteuid(), root.Uid)
}

func TestNamedLock_UnixDirectoryCloseFailure_ClosesOwnedLock(t *testing.T) {
	cases := []struct {
		name     string
		withHeld bool
	}{{"owned-held-file", true}, {"no-held-file", false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.Open(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, dir.Close())
			var held, opened *os.File
			if tc.withHeld {
				opened, err = os.CreateTemp(t.TempDir(), "owned-lock")
				require.NoError(t, err)
				held = opened
			}
			original := errors.New("prior acquisition refusal")
			result := original
			finishLockOpen(dir, &held, &result)
			require.ErrorIs(t, result, os.ErrClosed, "real repeated directory close remains an error")
			require.ErrorIs(t, result, original, "existing refusal survives close-error reconciliation")
			require.Nil(t, held, "a failed directory close cannot return an owned held descriptor")
			if opened != nil {
				_, err = opened.Stat()
				require.ErrorIs(t, err, os.ErrClosed, "the real owned file was closed on refusal")
			}
		})
	}
}

// TestNamedLock_UnixOpenLock_VerifiesDirectoryThenFile kills the mutant that
// skips the lock-file verification: the verifier seam must see the opened
// directory (directory=true) and then the opened lock file (directory=false),
// and a verifier refusal for the file must close the file and refuse.
func TestNamedLock_UnixOpenLock_VerifiesDirectoryThenFile(t *testing.T) {
	dir := t.TempDir()
	var calls []bool
	held, err := openLockWith(dir, "seam.lock", func(f *os.File, directory bool) error {
		calls = append(calls, directory)
		require.NoError(t, verifyLockObject(f, directory), "seam receives real opened objects")
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, held)
	require.NoError(t, held.Close())
	require.Equal(t, []bool{true, false}, calls, "directory is verified before the lock file, and the file is always verified")

	refusal := errors.New("file refused")
	before := openDescriptorCount(t)
	held, err = openLockWith(dir, "seam.lock", func(_ *os.File, directory bool) error {
		if directory {
			return nil
		}
		return refusal
	})
	require.ErrorIs(t, err, refusal)
	require.Nil(t, held)
	require.Equal(t, before, openDescriptorCount(t), "refused lock file is closed")
}

// TestAcquireNamed_ContentionRefusal_ClosesDescriptor kills the mutant that
// drops f.Close when the kernel lock attempt fails.
func TestAcquireNamed_ContentionRefusal_ClosesDescriptor(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1)) // a finalizer must not hide a leaked descriptor
	dir := t.TempDir()
	first, err := AcquireNamed(dir, "contended.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Release()) })
	before := openDescriptorCount(t)
	for range 20 {
		second, acquireErr := AcquireNamed(dir, "contended.lock")
		require.ErrorIs(t, acquireErr, ErrLockHeld)
		require.Nil(t, second)
	}
	require.Equal(t, before, openDescriptorCount(t), "every refused acquisition closes its descriptor")
}

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	dir, err := os.Open("/dev/fd")
	require.NoError(t, err)
	names, err := dir.Readdirnames(-1)
	require.NoError(t, err)
	require.NoError(t, dir.Close())
	return len(names)
}
