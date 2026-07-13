// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package hooks

import "syscall"

// detachedSysProcAttr detaches the hook command from the spawning console so
// it survives the parent exiting (MTIX-56.9). CREATE_NEW_PROCESS_GROUP is the
// Windows analogue of a new session for our purposes.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
