// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
)

// swap moves the store aside and seeds a fresh one; every failure from the move
// on restores the original (ADR-007 section 4.2 steps 2-5).
func (s *WorktreeEnrolment) swap(ctx context.Context, root string, r EnrollmentRequest, snap *WorktreeSnapshot, res *WorktreeEnrolResult) error {
	mtix := filepath.Join(root, ".mtix")
	data := filepath.Join(mtix, "data")
	if err := s.o.Rename(data, res.AsideDir); err != nil {
		return errors.Join(fmt.Errorf("move store aside: %w", err), removeEnrolMarker(mtix))
	}
	if err := s.seed(ctx, root, r.AcceptHub, snap, res); err != nil {
		return s.rollback(mtix, res, err)
	}
	if err := s.p().Enroll(ctx, r); err != nil {
		return s.afterEnrolFailure(ctx, root, res, fmt.Errorf("enroll worktree: %w", err))
	}
	return removeEnrolMarker(mtix)
}

func (s *WorktreeEnrolment) seed(ctx context.Context, root string, accept string, snap *WorktreeSnapshot, res *WorktreeEnrolResult) error {
	p := s.p()
	if err := p.Clone(ctx, root); err != nil {
		return fmt.Errorf("clone fresh store from the hub: %w", err)
	}
	if err := p.ImportCarry(ctx, root, snap.Carry, s.o.Clock().UTC()); err != nil {
		return fmt.Errorf("carry inbox state: %w", err)
	}
	// The fresh store has no sync.sha256: without one, the first command would
	// import tasks.json (the old store's export) over the hub clone. The board
	// is marked seen only after it is checked against the clone itself.
	mtix := filepath.Join(root, ".mtix")
	verify := func() error {
		f, err := p.VerifyBoard(ctx, root, res.AsideDir)
		return s.boardGate(mtix, accept, res, f, err)
	}
	if err := recordBoardSeen(mtix, verify); err != nil {
		return fmt.Errorf("record the board file as seen: %w", err)
	}
	if err := s.checkLostLocal(ctx, root, mtix, res); err != nil {
		return err
	}
	if err := s.recordHubAccepted(mtix, res); err != nil {
		return err
	}
	// A process that held the old store open writes through its descriptor into
	// the moved-aside directory; if the aside store differs from what was
	// carried, the carry is incomplete and the original must go back.
	now, err := p.Snapshot(ctx, res.AsideDir)
	if err != nil {
		return fmt.Errorf("re-read the moved-aside store: %w", err)
	}
	if !sameSnapshot(snap, now) {
		return fmt.Errorf("another process wrote to the worktree store during enrolment; nothing was lost, run the enrolment again: %w", model.ErrConflict)
	}
	return nil
}

// afterEnrolFailure decides between rollback and keeping the clone: when the
// host already records the enrolment, restoring the old store would leave the
// host enrolled on an unconverted store.
func (s *WorktreeEnrolment) afterEnrolFailure(ctx context.Context, root string, res *WorktreeEnrolResult, cause error) error {
	mtix := filepath.Join(root, ".mtix")
	enrolled, err := s.p().Enrolled(ctx, root)
	switch {
	case err != nil:
		return errors.Join(cause, fmt.Errorf("could not tell whether the enrolment was recorded: %w; the marker is kept and the original store is in %s, backup %s; run the enrolment again", err, res.AsideDir, res.BackupPath))
	case enrolled:
		return errors.Join(fmt.Errorf("the enrolment was recorded and the cloned store is in use (original store kept in %s, backup %s): %w", res.AsideDir, res.BackupPath, cause), removeEnrolMarker(mtix))
	}
	return s.rollback(mtix, res, cause)
}

// recordHubAccepted writes the accept-hub record when the enrolment took the
// hub's version of a differing board; doctor warns until the operator clears it.
func (s *WorktreeEnrolment) recordHubAccepted(mtix string, res *WorktreeEnrolResult) error {
	if res.HubDiffReport == "" && res.LostLocalReport == "" {
		return nil
	}
	report := res.HubDiffReport
	if report == "" {
		report = res.LostLocalReport
	}
	return writeHubAccepted(mtix, HubAccepted{Report: report, AsideDir: res.AsideDir, BackupPath: res.BackupPath,
		AcceptedAt: s.o.Clock().UTC().Format("2006-01-02T15:04:05Z"), LostLocalReport: res.LostLocalReport})
}

// checkLostLocal runs after the real clone: a field this store wrote itself that
// the clone, at the hub's head, holds differently is named in a report and in a
// notice, and the old store keeps the value. The enrolment goes on only with
// that notice. A failed check is an error, so the original store goes back; it
// is never a quiet pass (MTIX-112.17.16.18).
func (s *WorktreeEnrolment) checkLostLocal(ctx context.Context, root, mtix string, res *WorktreeEnrolResult) error {
	if s.o.Ports.LostLocal == nil {
		return nil
	}
	lost, err := s.o.Ports.LostLocal(ctx, root, res.AsideDir)
	if err != nil {
		return fmt.Errorf("check that the values written here survived the clone: %w", err)
	}
	if lost == nil || len(lost.Diff) == 0 {
		return nil
	}
	path, err := writeNamedReport(mtix, "enroll-lost-local-", s.o.Clock(), &BoardFinding{Reason: BoardLocalValueLost, Diff: lost.Diff})
	if err != nil {
		return err
	}
	res.LostLocal, res.LostLocalReport = lost, path
	if s.o.Notice != nil {
		if _, wErr := io.WriteString(s.o.Notice, res.Notice()); wErr != nil {
			return fmt.Errorf("print the lost-value notice: %w", wErr)
		}
	}
	return nil
}

// rollback keeps the half-built store aside and puts the original back. If the
// restore itself fails, the original is intact in the aside directory and the
// error names it. The marker is removed only after a successful restore.
func (s *WorktreeEnrolment) rollback(mtix string, res *WorktreeEnrolResult, cause error) error {
	data := filepath.Join(mtix, "data")
	if _, err := os.Lstat(data); err == nil {
		partial := filepath.Join(mtix, partialDirPrefix+filepath.Base(res.AsideDir)[len(asideDirPrefix):])
		if keepErr := s.keepPartial(data, partial); keepErr != nil {
			return errors.Join(cause, fmt.Errorf("%w; original store is intact in %s, backup %s", keepErr, res.AsideDir, res.BackupPath))
		}
		res.PartialDir = partial
	}
	if mvErr := s.o.Rename(res.AsideDir, data); mvErr != nil {
		return errors.Join(cause, fmt.Errorf("restore store: %w; original store is intact in %s, backup %s", mvErr, res.AsideDir, res.BackupPath))
	}
	if rmErr := errors.Join(removeHubAccepted(mtix), removeEnrolMarker(mtix)); rmErr != nil {
		return errors.Join(cause, rmErr)
	}
	return fmt.Errorf("%w (the original store was restored; backup %s)", cause, res.BackupPath)
}
