// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func directoryIdentity(path string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return "", fmt.Errorf("project identity: %w", err)
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func withApprovalLock(state *oplocal.State, _ string, action func() error) (err error) {
	if ensureErr := state.Ensure(); ensureErr != nil {
		return ensureErr
	}
	path, err := state.Path()
	if err != nil {
		return err
	}
	fd, err := unix.Open(filepath.Join(path, "hook-approvals.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("open approval lock: %w", err)
	}
	defer func() { err = errors.Join(err, unix.Close(fd)) }()
	var stat unix.Stat_t
	if statErr := unix.Fstat(fd, &stat); statErr != nil {
		return fmt.Errorf("approval lock metadata: %w", statErr)
	}
	effective := os.Geteuid()
	if effective < 0 || effective > math.MaxUint32 {
		return fmt.Errorf("approval lock user identity is invalid")
	}
	if stat.Uid != uint32(effective) || stat.Mode&07777 != 0600 || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("approval lock metadata is invalid")
	}
	if lockErr := unix.Flock(fd, unix.LOCK_EX); lockErr != nil {
		return fmt.Errorf("lock approvals: %w", lockErr)
	}
	defer func() { err = errors.Join(err, unix.Flock(fd, unix.LOCK_UN)) }()
	return action()
}
