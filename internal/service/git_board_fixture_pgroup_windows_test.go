// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package service

import "os/exec"

// isolateBoardGit is a no-op: the Git fixtures run on Linux and macOS only.
func isolateBoardGit(_ *exec.Cmd) {}
