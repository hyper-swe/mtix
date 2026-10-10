//go:build windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"strings"
)

func handlePath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	count, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", failure("canonical path is unavailable", err)
	}
	if count >= uint32(len(buffer)) {
		return "", invalid("canonical path is too long")
	}
	return strings.TrimPrefix(windows.UTF16ToString(buffer[:count]), `\\?\`), nil
}
func canonicalRoots(roots []string) ([]string, error) {
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		text, err := windows.UTF16PtrFromString(root)
		if err != nil {
			return nil, failure("configured path is invalid", err)
		}
		handle, err := windows.CreateFile(text, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if errors.Is(err, os.ErrNotExist) {
			result = append(result, root)
			continue
		}
		if err != nil {
			return nil, failure("configured path is unavailable", err)
		}
		canonical, pathErr := handlePath(handle)
		closeErr := windows.CloseHandle(handle)
		if pathErr != nil || closeErr != nil {
			return nil, failure("configured path metadata is unavailable", errors.Join(pathErr, closeErr))
		}
		result = append(result, canonical)
	}
	return result, nil
}
func checkHandleLocation(dir *os.File, roots []string) error {
	current, err := handlePath(windows.Handle(dir.Fd()))
	if err != nil {
		return err
	}
	for _, root := range roots {
		if inside("windows", current, root) {
			return invalid("directory must be outside " + root)
		}
	}
	return nil
}
