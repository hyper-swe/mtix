//go:build linux || darwin

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Supported POSIX installations require executable ownership and directory trust.
package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func selectBoardGit() (string, error) {
	for _, path := range []string{"/usr/bin/git", "/usr/local/bin/git", "/opt/homebrew/bin/git"} {
		resolved, err := trustedBoardGit(path, "/")
		if err == nil {
			return resolved, nil
		}
	}
	return "", gitBoardError("trusted Git unavailable", nil)
}

func trustedBoardGit(path, trustRoot string) (string, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(trustRoot) {
		return "", gitBoardError("untrusted Git location", nil)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", gitBoardError("Git executable unavailable", nil)
	}
	if trustRoot == "/" && !boardInstallation(resolved) {
		return "", gitBoardError("untrusted Git location", nil)
	}
	relative, err := filepath.Rel(trustRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", gitBoardError("untrusted Git location", nil)
	}
	if err := boardOwnedMode(resolved, false); err != nil {
		return "", err
	}
	for dir := filepath.Dir(resolved); ; dir = filepath.Dir(dir) {
		if err := boardOwnedMode(dir, true); err != nil {
			return "", err
		}
		if dir == trustRoot {
			break
		}
	}
	return resolved, nil
}

func boardInstallation(path string) bool {
	for _, prefix := range []string{"/usr/bin/", "/usr/local/", "/opt/homebrew/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func boardOwnedMode(path string, directory bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return gitBoardError("Git trust metadata unavailable", nil)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && int64(stat.Uid) != int64(os.Getuid())) {
		return gitBoardError("untrusted Git owner", nil)
	}
	if info.Mode().Perm()&0022 != 0 {
		return gitBoardError("untrusted Git mode", nil)
	}
	if directory {
		if !info.IsDir() {
			return gitBoardError("untrusted Git directory", nil)
		}
		return nil
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return gitBoardError("untrusted Git executable", nil)
	}
	return nil
}

func containBoardProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killBoardGroup(cmd) }
	return nil
}

func killBoardGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func cleanupBoardProcess(cmd *exec.Cmd) error {
	// Even successful direct processes must not leave a helper in their owned group.
	err := killBoardGroup(cmd)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
