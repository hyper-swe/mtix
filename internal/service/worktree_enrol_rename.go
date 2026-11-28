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
//
// Accepted order (founder decision DESK-1.248, MTIX-112.17.16.21.2.2): the
// object is moved first and its type re-checked after, not locked or re-checked
// before. The window is the span between the caller's type check and the
// re-check, a few microseconds, and needs three actors: a live enrolment that
// creates a regular lock inside it, then a third process that creates a fresh
// lock at the lock path. The held lock then sits under the aside name, and the
// fresh lock is a different inode, so two holders are briefly possible. A racing
// process can achieve no more than that: it cannot remove or replace the held
// lock (RENAME_NOREPLACE never overwrites the aside, the put-back never
// overwrites the fresh lock), and the move itself fails closed with ErrConflict
// (and ErrAlreadyExists when the put-back is refused), naming where the held
// lock is. The lock only serializes enrolments and writes no data, so the
// overlap loses nothing, and it needs a process that takes the lock after the
// operator started the fix; the operator docs say to run the fix with no other
// mtix process in the worktree.
// TestMoveEnrolLockAside_ThirdActorLockInWindow_HeldLockStaysAside pins this.
func moveEnrolLockAside(lock, aside string) error {
	return moveEnrolLockAsideWith(lock, aside, os.Lstat)
}

// moveEnrolLockAsideWith is moveEnrolLockAside with the post-move check
// injected, so a test can make that check fail without package state.
func moveEnrolLockAsideWith(lock, aside string, lstat func(string) (os.FileInfo, error)) error {
	if err := renameNoReplace(lock, aside); err != nil {
		return fmt.Errorf("move enrolment lock aside: %w", err)
	}
	info, err := lstat(aside)
	if err != nil {
		return fmt.Errorf("verify moved enrolment lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if backErr := renameThenRecheck(aside, lock); backErr != nil {
		return fmt.Errorf("%s became a regular file during the move and could not be put back (it is now %s): %w", lock, aside, errors.Join(model.ErrConflict, backErr))
	}
	return fmt.Errorf("refusing to move %s: it became a regular file during the move and was put back: %w", lock, model.ErrConflict)
}
