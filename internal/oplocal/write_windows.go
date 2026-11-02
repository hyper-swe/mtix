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

// writeData retains existing typed callers and tests while they migrate to
// writeDataMode options. Retire this compatibility signature in MTIX-112.22.1.
func writeData(dir *os.File, name string, data []byte, exclusive bool, before func() error, _ func(*os.File) error) error {
	return writeDataMode(dir, name, data, writeOptions{exclusive: exclusive, before: before, mode: recordMode})
}

func writeDataMode(dir *os.File, name string, data []byte, opts writeOptions) (err error) {
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
		err = flushStateFile(f, opts.flushNative)
	}
	if err == nil && opts.before != nil {
		err = opts.before()
	}
	if err == nil {
		err = renameFile(dir, f, name, opts.exclusive)
	}
	if err != nil {
		return writeStateError(err)
	}
	// Publication has completed; a subsequent flush failure must keep the record.
	committed = true
	if err = flushStateFile(f, opts.flushNative); err != nil {
		return writeStateError(err)
	}
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

// removeData deletes one state record relative to the verified directory.
func removeData(dir *os.File, name string) (err error) {
	f, err := relativeOpen(dir, name, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_OPEN, windows.GENERIC_READ|windows.DELETE)
	if err != nil {
		return fmt.Errorf("open state record: %w: %w", err, invalid("remove failed"))
	}
	defer closeWith(f, &err)
	if err = verifyFile(f, false); err != nil {
		return err
	}
	return discardFile(f)
}
