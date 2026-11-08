// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/model"
)

// renameThenRecheck is the portable no-replace move for systems without an
// atomic one: it refuses an existing target, renames, and the caller re-checks
// what moved. The window between the check and the rename is the one the
// platform primitive (renameNoReplace on Linux) closes (MTIX-112.17.16.21.2).
func renameThenRecheck(oldPath, newPath string) error {
	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("refusing to move %s: %s already exists: %w", oldPath, newPath, model.ErrAlreadyExists)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %s: %w", oldPath, err)
	}
	return nil
}

// moveEnrolLockAside moves a non-regular lock object to aside without ever
// replacing an existing aside, then re-checks what was moved. If the object that
// moved is a regular file, an enrolment created a real lock after the type
// check: it is put back and the move is refused, so a held lock is never moved
// (MTIX-112.17.16.21.2).
func moveEnrolLockAside(lock, aside string) error {
	if err := renameNoReplace(lock, aside); err != nil {
		return fmt.Errorf("move enrolment lock aside: %w", err)
	}
	info, err := os.Lstat(aside)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if backErr := renameThenRecheck(aside, lock); backErr != nil {
		return fmt.Errorf("%s became a regular file during the move and could not be put back (it is now %s): %w", lock, aside, errors.Join(model.ErrConflict, backErr))
	}
	return fmt.Errorf("refusing to move %s: it became a regular file during the move and was put back: %w", lock, model.ErrConflict)
}
