// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openAlertLock(dir *os.File, create bool) (*os.File, error) {
	return openNamedLock(dir, "alerts.lock", create)
}

func openNamedLock(dir *os.File, name string, create bool) (*os.File, error) {
	if create {
		if err := publishNamedLock(dir, name); err != nil {
			return nil, err
		}
	}
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Openat(checkedFD(dir), name, flags, 0600)
	if err != nil {
		return nil, err
	}
	f := descriptorFile(fd, filepath.Join(dir.Name(), name))
	if err = verifyFile(f, false); err != nil {
		return nil, errors.Join(err, closeFile(f))
	}
	return f, nil
}
func tryAlertLock(f *os.File) error { return unix.Flock(checkedFD(f), unix.LOCK_EX|unix.LOCK_NB) }
func busyAlertLock(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
func unlockAlert(f *os.File) error {
	return errors.Join(unix.Flock(checkedFD(f), unix.LOCK_UN), closeFile(f))
}

func publishNamedLock(dir *os.File, name string) error {
	_, err := readData(dir, name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Exclusive protected publication creates one stable inode; existing locks are never replaced.
	err = writeData(dir, name, []byte{}, true, nil, nil)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}
