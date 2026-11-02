// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteExecutable atomically writes a release-defined executable (mode 0700,
// owned by the operator) into the operator directory and returns its path.
// It is for bytes that carry no authority, such as the attach shim
// (ADR-007 §6.5); grants stay in JSON records.
func (s *State) WriteExecutable(name string, data []byte) (path string, err error) {
	file, err := executableName(name)
	if err != nil {
		return "", err
	}
	if len(data) > maxRecordSize {
		return "", invalid("executable is too large")
	}
	dir, err := s.directory(true)
	if err != nil {
		return "", err
	}
	defer closeWith(dir, &err)
	opts := writeOptions{before: s.beforeCommit, syncDirectory: s.syncDirectory, mode: executableMode, flushNative: s.flushNative}
	if err = writeDataMode(dir, file, data, opts); err != nil {
		return "", fmt.Errorf("write executable %s: %w", file, err)
	}
	root, err := s.resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, file), nil
}

// ReadExecutable reads a release-defined executable after verifying its owner,
// type and mode 0700. It returns its path, and an error matching
// os.ErrNotExist when the directory or the file is absent.
func (s *State) ReadExecutable(name string) (data []byte, path string, err error) {
	file, err := executableName(name)
	if err != nil {
		return nil, "", err
	}
	root, err := s.resolve()
	if err != nil {
		return nil, "", err
	}
	path = filepath.Join(root, file)
	dir, err := s.directory(false)
	if missingState(err) {
		return nil, path, fmt.Errorf("read executable %s: %w", file, os.ErrNotExist)
	}
	if err != nil {
		return nil, path, err
	}
	defer closeWith(dir, &err)
	data, err = readDataMode(dir, file, executableMode)
	if err != nil {
		return nil, path, fmt.Errorf("read executable %s: %w", file, err)
	}
	return data, path, nil
}
