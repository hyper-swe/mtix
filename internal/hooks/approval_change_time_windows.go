// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package hooks

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileBasicInfo mirrors FILE_BASIC_INFO.
type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

// windowsEpochOffset is the 100ns tick count between 1601 and 1970.
const windowsEpochOffset = 116444736000000000

// changeTime reads ChangeTime from the opened handle (FR-19).
func changeTime(f *os.File) (time.Time, error) {
	var info fileBasicInfo
	err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, (info.ChangeTime-windowsEpochOffset)*100), nil
}
