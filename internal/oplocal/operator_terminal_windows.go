// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"golang.org/x/sys/windows"
	"os"
)

// OperatorTerminal checks whether stdin is a Windows console handle.
func OperatorTerminal(stdin *os.File) bool {
	if stdin == nil {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(stdin.Fd()), &mode) == nil
}
