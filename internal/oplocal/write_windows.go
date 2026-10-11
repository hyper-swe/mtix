// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func writeData(dir *os.File, name string, data []byte, exclusive bool, before func() error, _ func(*os.File) error) (err error) {
	_, err = readData(dir, name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return failure("temporary name is unavailable", err)
	}
	temp := ".state-" + hex.EncodeToString(random)
	f, err := relativeOpen(dir, temp, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_CREATE, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE)
	if err != nil {
		return fmt.Errorf("create state: %w: %w", err, invalid("write failed"))
	}
	defer closeWith(f, &err)
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, discardFile(f))
		}
	}()
	if err = verifyFile(f, false); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil && before != nil {
		err = before()
	}
	if err == nil {
		err = renameFile(dir, f, name, exclusive)
	}
	if err != nil {
		return fmt.Errorf("write state: %w: %w", err, invalid("write failed"))
	}
	committed = true
	return nil
}

func discardFile(f *os.File) error {
	var result windows.IO_STATUS_BLOCK
	value := byte(1)
	return ntError(windows.NtSetInformationFile(windows.Handle(f.Fd()), &result, &value, 1, windows.FileDispositionInformation))
}

func renameFile(dir, f *os.File, name string, exclusive bool) error {
	text, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	var info struct {
		Replace uint32
		Root    windows.Handle
		Length  uint32
		Name    [256]uint16
	}
	if len(text) > len(info.Name) {
		return invalid("record name is invalid")
	}
	if !exclusive {
		info.Replace = windows.FILE_RENAME_REPLACE_IF_EXISTS
	}
	info.Root = windows.Handle(dir.Fd())
	info.Length = uint32((len(text) - 1) * 2)
	copy(info.Name[:], text)
	size := uint32(unsafe.Offsetof(info.Name)) + info.Length
	var result windows.IO_STATUS_BLOCK
	return ntError(windows.NtSetInformationFile(windows.Handle(f.Fd()), &result, (*byte)(unsafe.Pointer(&info)), size, windows.FileRenameInformation))
}
