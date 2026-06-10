// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package sqlite

import (
	"fmt"
	"syscall"
	"unsafe"
)

// freeDiskSpace returns the bytes available to the calling user on the
// volume holding dir, via GetDiskFreeSpaceExW.
func freeDiskSpace(dir string) (uint64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")

	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, fmt.Errorf("encode path %s: %w", dir, err)
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	r1, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW %s: %w", dir, callErr)
	}
	return freeBytesAvailable, nil
}
