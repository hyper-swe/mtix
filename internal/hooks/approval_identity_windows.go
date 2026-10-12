// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package hooks

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func directoryIdentity(path string) (identity string, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", fmt.Errorf("project identity: %w", err)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(handle)) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func withApprovalLock(state *oplocal.State, project string, action func() error) (err error) {
	if err := state.Ensure(); err != nil {
		return err
	}
	path, err := state.Path()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString("Global\\" + recordKey("mtix-", project, path))
	if err != nil {
		return err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return fmt.Errorf("open approval lock: %w", err)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(handle)) }()
	result, err := windows.WaitForSingleObject(handle, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("lock approvals: %w", err)
	}
	if result != windows.WAIT_OBJECT_0 && result != windows.WAIT_ABANDONED {
		return fmt.Errorf("approval lock unavailable")
	}
	defer func() { err = errors.Join(err, windows.ReleaseMutex(handle)) }()
	return action()
}
