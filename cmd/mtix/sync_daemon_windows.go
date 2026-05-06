// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import "syscall"

// syscall0 on Windows returns Interrupt. Windows doesn't honor signal
// 0 the same way Unix does; this is a best-effort liveness probe.
func syscall0() syscall.Signal {
	return syscall.SIGINT
}
