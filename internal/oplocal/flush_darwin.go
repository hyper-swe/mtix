// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"log/slog"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

type darwinFlushCalls struct {
	fullSync func(uintptr) error
	fileSync func(uintptr) error
	warn     func(error)
}

func darwinPlatformCalls(warn func(string, ...any)) darwinFlushCalls {
	return darwinFlushCalls{fullSync: fullSyncFD, fileSync: fileSyncFD, warn: func(err error) {
		warn("operator state full sync unsupported; attempting fsync", "error", err)
	}}
}

func platformFlush(f *os.File) error {
	calls := darwinPlatformCalls(slog.Warn)
	return withFileHandle(f, func(fd uintptr) error { return flushDarwinHandle(fd, calls) })
}

func flushDarwinHandle(fd uintptr, calls darwinFlushCalls) error {
	err := retryNativeFlush(func() error { return calls.fullSync(fd) })
	if !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	calls.warn(err)
	return retryNativeFlush(func() error { return calls.fileSync(fd) })
}

func retryNativeFlush(flush func() error) error {
	for {
		err := flush()
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func fullSyncFD(fd uintptr) error {
	_, err := unix.FcntlInt(fd, unix.F_FULLFSYNC, 0)
	return err
}

func fileSyncFD(fd uintptr) error {
	if fd > math.MaxInt32 {
		return unix.EBADF
	}
	return unix.Fsync(int(fd))
}
