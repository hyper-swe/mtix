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
	path := windows.UTF16ToString(buffer[:count])
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(path, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(path, `\\?\`), nil
}
func canonicalRoots(roots []string) ([]string, error) {
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		canonical, err := canonicalExclusion(root, canonicalExistingPath)
		if err != nil {
			logExcludedRoot(root, err)
			continue
		}
		result = append(result, canonical)
	}
	return result, nil
}
func canonicalExistingPath(path string) (string, error) {
	text, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(text, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	canonical, pathErr := handlePath(handle)
	return canonical, errors.Join(pathErr, windows.CloseHandle(handle))
}
func checkWindowsLocation(target string, roots []string) error {
	canonical, err := canonicalLocation(target)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if inside("windows", canonical, root) {
			return invalid("directory must be outside " + root)
		}
	}
	return nil
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
