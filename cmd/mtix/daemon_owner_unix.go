// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

func platformAcquireDaemonLock(file *os.File) error {
	fd := file.Fd()
	if fd > uintptr(^uint(0)>>1) {
		return syscall.EBADF
	}
	err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // descriptor bounds checked above
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errDaemonLockHeld
	}
	return err
}
