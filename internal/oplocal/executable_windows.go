// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"fmt"
	"os"
)

// WriteExecutable is unsupported on Windows: the attach shim is a POSIX sh
// script and built-in wakes are unavailable there (ADR-007 §2.10).
func (s *State) WriteExecutable(name string, _ []byte) (string, error) {
	if _, err := executableName(name); err != nil {
		return "", err
	}
	return "", fmt.Errorf("write executable %s: %w", name, ErrUnsupportedPlatform)
}

// ReadExecutable reports every executable as absent on Windows, where none
// is ever written.
func (s *State) ReadExecutable(name string) ([]byte, string, error) {
	if _, err := executableName(name); err != nil {
		return nil, "", err
	}
	return nil, "", fmt.Errorf("read executable %s: %w", name, os.ErrNotExist)
}
