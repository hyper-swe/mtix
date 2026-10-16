// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"golang.org/x/sys/unix"
	"math"
	"os"
)

// OperatorTerminal checks the actual terminal protocol, not character-device mode.
func OperatorTerminal(stdin *os.File) bool {
	if stdin == nil {
		return false
	}
	fd := stdin.Fd()
	if fd > math.MaxInt {
		return false
	}
	_, err := unix.IoctlGetTermios(int(fd), operatorTerminalRequest)
	return err == nil
}
