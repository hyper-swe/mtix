// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"os/exec"
	"syscall"
)

// isolateBoardGit puts the fixture Git call in its own process group and makes
// cancellation kill the whole group, so a helper process Git spawned cannot
// outlive the bound.
func isolateBoardGit(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid signals the process group led by the Git call.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
