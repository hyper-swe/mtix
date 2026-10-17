// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"os"

	"golang.org/x/sys/windows"
)

func platformFlush(f *os.File) error {
	return withFileHandle(f, func(fd uintptr) error { return flushWindowsHandle(fd, windows.FlushFileBuffers) })
}

func flushWindowsHandle(fd uintptr, flush func(windows.Handle) error) error {
	return flush(windows.Handle(fd))
}
