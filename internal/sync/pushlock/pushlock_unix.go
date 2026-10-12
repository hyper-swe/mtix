// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package pushlock

import (
	"errors"
	"fmt"
	"math"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// errPlatformLockHeld is the platform-specific sentinel returned when
// the flock attempt fails because another process holds the lock.
// pushlock.Acquire translates this into the public ErrLockHeld.
var errPlatformLockHeld = errors.New("platform: lock held")

// platformAcquire attempts a non-blocking exclusive flock on f.
// EWOULDBLOCK / EAGAIN both signal contention; any other syscall error
// is propagated verbatim (caller wraps with context).
func platformAcquire(f *os.File) error {
	if err := syscall.Flock(fdInt(f), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errPlatformLockHeld
		}
		return err
	}
	return nil
}

// platformRelease unlocks the flock. Closing the fd would also release
// the lock (kernel guarantee), but explicit unlock keeps the intent
// visible and short-circuits on any unlock error before close runs.
func platformRelease(f *os.File) error {
	return syscall.Flock(fdInt(f), syscall.LOCK_UN)
}

// fdInt narrows the file descriptor to int with bounds protection.
// On all supported Unix systems file descriptors fit in int comfortably;
// the bounds check is a defensive nicety for static analyzers.
func fdInt(f *os.File) int {
	fd := f.Fd()
	if fd > uintptr(^uint(0)>>1) {
		return -1
	}
	return int(fd) //nolint:gosec // bounds-checked above
}

// pushLockMode is the file permission mode used when creating the lock
// file. 0600 keeps it user-readable only; the lock file is local process
// coordination, never a credential.
const pushLockMode os.FileMode = 0o600

// platformEnsureDir creates the lock data directory when it is absent.
// Unix ownership follows the effective user through the process umask.
func platformEnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func platformOpenLock(path, name string) (*os.File, error) {
	return openLockWith(path, name, verifyLockObject)
}

// openLockWith opens the data directory and lock file without following
// symlinks, verifying each opened object through verify (directory=true for the
// directory, false for the lock file). The seam is a parameter, never state, so
// production always passes verifyLockObject.
func openLockWith(path, name string, verify func(*os.File, bool) error) (held *os.File, err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open real data directory: %w", err)
	}
	dir, err := lockDescriptorFile(fd, path)
	if err != nil {
		return nil, err
	}
	defer finishLockOpen(dir, &held, &err)
	if verifyErr := verify(dir, true); verifyErr != nil {
		return nil, verifyErr
	}
	fd, err = openNamedLockAt(fd, name)
	if err != nil {
		return nil, fmt.Errorf("open real lock object: %w", err)
	}
	f, err := lockDescriptorFile(fd, name)
	if err != nil {
		return nil, err
	}
	if err := verify(f, false); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func verifyLockObject(f *os.File, directory bool) error {
	var metadata unix.Stat_t
	if err := unix.Fstat(fdInt(f), &metadata); err != nil {
		return fmt.Errorf("lock metadata: %w", err)
	}
	return validateLockMetadata(metadata.Uid, uint32(metadata.Mode), os.Geteuid(), directory)
}

func validateLockMetadata(owner, mode uint32, effective int, directory bool) error {
	kind := uint32(unix.S_IFREG)
	if directory {
		kind = unix.S_IFDIR
	}
	if effective < 0 || effective > math.MaxUint32 || owner != uint32(effective) || mode&unix.S_IFMT != kind {
		return fmt.Errorf("lock object must have expected type and effective user ownership")
	}
	return nil
}

func lockDescriptorFile(fd int, name string) (*os.File, error) {
	if fd < 0 {
		return nil, fmt.Errorf("lock descriptor is invalid")
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Create exclusively, then reopen an existing name through the same retained
// directory handle. Every open keeps no-follow and nonblocking type validation;
// ENOENT remains an error, never a path fallback or unbounded retry.
func openNamedLockAt(parent int, name string) (int, error) {
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Openat(parent, name, flags|unix.O_CREAT|unix.O_EXCL, uint32(pushLockMode))
	if errors.Is(err, unix.EEXIST) {
		return unix.Openat(parent, name, flags, 0)
	}
	return fd, err
}

// finishLockOpen preserves directory-close errors and disposes of any lock
// handle whose acquisition cannot be returned successfully.
func finishLockOpen(dir *os.File, held **os.File, retErr *error) {
	if closeErr := dir.Close(); closeErr != nil {
		*retErr = errors.Join(*retErr, closeErr)
		if *held != nil {
			*retErr = errors.Join(*retErr, (*held).Close())
			*held = nil
		}
	}
}
