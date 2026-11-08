// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package service

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/hyper-swe/mtix/internal/model"
)

// renameNoReplace renames oldPath to newPath and fails with ErrAlreadyExists if
// newPath exists, atomically (renameat2 RENAME_NOREPLACE). A kernel or file
// system without it falls back to renameThenRecheck.
func renameNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("refusing to move %s: %s already exists: %w", oldPath, newPath, model.ErrAlreadyExists)
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTSUP):
		return renameThenRecheck(oldPath, newPath)
	default:
		return fmt.Errorf("rename %s: %w", oldPath, err)
	}
}
