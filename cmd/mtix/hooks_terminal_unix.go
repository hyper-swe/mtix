// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"math"
	"os"

	"golang.org/x/sys/unix"
)

func hookStdinTerminal() bool {
	fd := os.Stdin.Fd()
	if fd > math.MaxInt {
		return false
	}
	_, err := unix.IoctlGetTermios(int(fd), terminalRequest)
	return err == nil
}
