// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package hooks

import (
	"fmt"
	"os"
	"syscall"
)

// Windows has no non-blocking open flag; containment is enforced by the root
// handle, and the final component by the exclusive create and the post-open
// same-file check.
const (
	nonBlockFlag = 0
)

func clearNonBlock(*os.File) error { return nil }

// linkCount reports 1 for a path inspected before the open; the link count of
// the opened handle is checked by openedLinkCount.
func linkCount(os.FileInfo) uint64 { return 1 }

// wideMode is always false: Windows permission bits do not describe access.
func wideMode(os.FileInfo) bool { return false }

// openedLinkCount reads the link count from the open handle, the cheap and
// race-free place to ask. A failed query is returned, never assumed to be 1.
func openedLinkCount(f *os.File, _ os.FileInfo) (uint64, error) {
	return handleLinkCount(f, queryHandleInformation)
}

// queryHandleInformation asks Windows for the information of an open handle.
func queryHandleInformation(fd uintptr, data *syscall.ByHandleFileInformation) error {
	return syscall.GetFileInformationByHandle(syscall.Handle(fd), data)
}

// handleLinkCount runs query on the handle of f; the query is a parameter so a
// failure can be tested.
func handleLinkCount(f *os.File, query func(uintptr, *syscall.ByHandleFileInformation) error) (uint64, error) {
	conn, err := f.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("get file handle: %w", err)
	}
	var data syscall.ByHandleFileInformation
	var queryErr error
	if err := conn.Control(func(fd uintptr) { queryErr = query(fd, &data) }); err != nil {
		return 0, fmt.Errorf("use file handle: %w", err)
	}
	if queryErr != nil {
		return 0, fmt.Errorf("query file information: %w", queryErr)
	}
	return uint64(data.NumberOfLinks), nil
}
