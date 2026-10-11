//go:build !linux && !darwin

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Platforms without the installation and process-group policy fail conservatively.
package service

import "os/exec"

func selectBoardGit() (string, error) {
	return "", gitBoardError("Git policy unsupported on this platform", nil)
}
func containBoardProcess(_ *exec.Cmd) error {
	return gitBoardError("Git process policy unsupported on this platform", nil)
}
func cleanupBoardProcess(_ *exec.Cmd) error { return nil }

func trustedBoardGit(_, _ string) (string, error) {
	return "", gitBoardError("Git policy unsupported on this platform", nil)
}
