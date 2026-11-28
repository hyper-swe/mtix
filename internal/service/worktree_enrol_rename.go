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
// platform primitive (renameNoReplace on Linux with renameat2) closes for the
// aside step only; the put-back always uses this function and keeps the window
// (MTIX-112.17.16.21.2).
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
// lock at the lock path. The put-back is renameThenRecheck on every platform: a
// check (Lstat) then a rename, which is not atomic. Only the aside step is
// no-replace atomic, and only on Linux with renameat2 (RENAME_NOREPLACE); the
// other platforms, and Linux when renameat2 is unavailable (ENOSYS, EINVAL,
// ENOTSUP), use the same check-then-rename for it.
//
// What happens: when the put-back sees the fresh lock it is refused. The move
// then fails with ErrConflict and ErrAlreadyExists, naming where the held lock
// is, and the held lock stays under the aside name for the rest of that
// enrolment. The fresh lock is a different inode, so the held lock no longer
// guards the lock path: later ProbeEnrolLock calls see the lock as free,
// CheckEnrolGuard lets other commands write until the enrolment writes its
// marker, and a second enrolment can acquire the lock and run at the same time.
// This is not brief and not shown harmless; it lasts until the live enrolment
// ends. Residual window: a lock created between the put-back's check and its
// rename is replaced, so two holders can exist. Doctor does not report the aside
// file afterwards; its only name for it is the fix's error. The operator docs
// say to run the fix with no other mtix process in the worktree and one
// enrolment at a time.
// TestMoveEnrolLockAside_ThirdActorLockInWindow_HeldLockStaysAside pins the
// refused put-back (a fresh lock that exists before the put-back's check), not
// the window between that check and the rename.
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
