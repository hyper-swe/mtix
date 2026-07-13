// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import "syscall"

// detachedSysProcAttr puts the hook command in its own session so it survives
// the spawning process exiting (an ephemeral CLI trigger) and is not killed by
// the parent's signal group on daemon shutdown (MTIX-56.9).
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
