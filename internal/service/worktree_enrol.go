// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// asideDirPrefix names the directory the old store moves to; ADR-007 section 4.2
// fixes the on-disk name.
const asideDirPrefix = "data.pre-enrol-" //nolint:misspell // on-disk name fixed by ADR-007 section 4.2

// WorktreeSnapshot is what the enrolment reads from the worktree's existing store.
type WorktreeSnapshot struct {
	// HubConnected is the store's hub-connected mark.
	HubConnected bool
	Events       []sqlite.LocalEvent
	Carry        *sqlite.InboxCarry
}

// WorktreePorts are the effects of the procedure. The production set lives in
// cmd/mtix; there is deliberately no port that discards local state (ADR-007 D14).
type WorktreePorts struct {
	// Snapshot opens the store read-only in effect and closes it again.
	Snapshot func(ctx context.Context, root string) (*WorktreeSnapshot, error)
	// HubMissing returns the ids among ids that the hub does not hold, using the
	// worktree's own secrets.
	HubMissing func(ctx context.Context, root string, ids []string) ([]string, error)
	// Backup writes a verified backup of the store to dest.
	Backup func(ctx context.Context, root, dest string) error
	// Clone seeds a fresh store at <root>/.mtix/data from the hub with the
	// worktree's own secrets.
	Clone func(ctx context.Context, root string) error
	// ImportCarry writes the carried inbox state into the cloned store.
	ImportCarry func(ctx context.Context, root string, carry *sqlite.InboxCarry, at time.Time) error
	// Enroll records the enrolment (HostEnrollment.Enroll).
	Enroll func(ctx context.Context, r EnrollmentRequest) error
}

// WorktreeEnrolOptions configures the enrolment.
type WorktreeEnrolOptions struct {
	Ports WorktreePorts
	Clock func() time.Time
	// Rename and RemoveTree default to the os operations; tests inject failures.
	Rename     func(oldPath, newPath string) error
	RemoveTree func(path string) error
}

// WorktreeEnrolResult reports what the enrolment did.
type WorktreeEnrolResult struct {
	BackupPath string
	AsideDir   string
	Carried    sqlite.InboxCarryResult
}

// WorktreeEnrolment enrolls a hub-board worktree that already has a store
// (ADR-007 section 4.2, D14 option 2, FR-20.4).
type WorktreeEnrolment struct{ o WorktreeEnrolOptions }

// NewWorktreeEnrolment builds the service.
func NewWorktreeEnrolment(o WorktreeEnrolOptions) *WorktreeEnrolment {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Rename == nil {
		o.Rename = os.Rename
	}
	if o.RemoveTree == nil {
		o.RemoveTree = removeTreeFilesFirst
	}
	return &WorktreeEnrolment{o: o}
}

// EnrolWorktree runs the ADR-007 section 4.2 procedure for the worktree at
// r.Path: refuse when local-only events exist, back up, move the store aside,
// clone fresh with the worktree's own secrets, carry the inbox state by event
// id, enroll. Any failure after the move restores the original store as it was.
// It never discards local state.
func (s *WorktreeEnrolment) EnrolWorktree(ctx context.Context, r EnrollmentRequest) (*WorktreeEnrolResult, error) {
	root, err := s.validate(r)
	if err != nil {
		return nil, err
	}
	// The lock lives under .mtix/enroll.d, not in .mtix/data: the procedure moves
	// the data directory, and a lock inside it would move with it.
	lock, err := pushlock.AcquireNamed(filepath.Join(root, ".mtix", "enroll.d"), "enroll.lock")
	if err != nil {
		return nil, fmt.Errorf("lock worktree enrolment: %w", err)
	}
	res, runErr := s.run(ctx, root, r)
	if relErr := lock.Release(); relErr != nil {
		runErr = errors.Join(runErr, relErr)
	}
	return res, runErr
}

func (s *WorktreeEnrolment) validate(r EnrollmentRequest) (string, error) {
	p := s.o.Ports
	if p.Snapshot == nil || p.HubMissing == nil || p.Backup == nil || p.Clone == nil || p.ImportCarry == nil || p.Enroll == nil {
		return "", fmt.Errorf("worktree enrolment ports required: %w", model.ErrInvalidInput)
	}
	root, err := canonicalEnrollmentPath(r.Path)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	info, statErr := os.Lstat(filepath.Join(root, ".git"))
	if statErr != nil || !info.Mode().IsRegular() ||
		oplocal.ValidateLaneLocation(oplocal.LaneLocation{Harness: "dispatcher", Root: root, Path: root}) != nil {
		return "", fmt.Errorf("%s is not a git worktree: %w", root, model.ErrInvalidInput)
	}
	return root, nil
}

func (s *WorktreeEnrolment) run(ctx context.Context, root string, r EnrollmentRequest) (*WorktreeEnrolResult, error) {
	snap, err := s.p().Snapshot(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("read worktree store: %w", err)
	}
	if !snap.HubConnected {
		return nil, fmt.Errorf("%s is not a hub-board worktree: %w", root, model.ErrInvalidInput)
	}
	if err := s.refuseLocalOnly(ctx, root, snap.Events); err != nil {
		return nil, err
	}
	stamp := s.o.Clock().UTC().Format("20060102T150405Z")
	res := &WorktreeEnrolResult{
		BackupPath: filepath.Join(root, ".mtix", "backups", "pre-enroll-"+stamp+".db"),
		AsideDir:   filepath.Join(root, ".mtix", asideDirPrefix+stamp),
	}
	if err := os.MkdirAll(filepath.Dir(res.BackupPath), 0o700); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}
	if err := s.p().Backup(ctx, root, res.BackupPath); err != nil {
		return nil, fmt.Errorf("back up worktree store: %w", err)
	}
	return res, s.swap(ctx, root, r, snap, res)
}

func (s *WorktreeEnrolment) p() WorktreePorts { return s.o.Ports }
