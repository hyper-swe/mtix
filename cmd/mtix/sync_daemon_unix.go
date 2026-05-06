// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import "syscall"

// syscall0 returns the no-op signal used by pidLive on Unix.
// Sending signal 0 reports the target's existence without delivering
// anything.
func syscall0() syscall.Signal {
	return syscall.Signal(0)
}
