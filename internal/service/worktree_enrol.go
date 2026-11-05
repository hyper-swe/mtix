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
	// Snapshot reads the store in the data directory dataDir and closes it again.
	Snapshot func(ctx context.Context, dataDir string) (*WorktreeSnapshot, error)
	// HubMissing returns the ids among ids that the hub does not hold, using the
	// worktree's own secrets.
	HubMissing func(ctx context.Context, root string, ids []string) ([]string, error)
	// Backup writes a verified backup of the store to dest.
	Backup func(ctx context.Context, root, dest string) error
	// Clone seeds a fresh store at <root>/.mtix/data from the hub with the
	// worktree's own secrets. It refuses a target that already holds events.
	Clone func(ctx context.Context, root string) error
	// ImportCarry writes the carried inbox state into the cloned store.
	ImportCarry func(ctx context.Context, root string, carry *sqlite.InboxCarry, at time.Time) error
	// Enroll records the enrolment (HostEnrollment.Enroll).
	Enroll func(ctx context.Context, r EnrollmentRequest) error
	// Enrolled reports whether the host records the worktree as enrolled; it
	// decides whether a failed Enroll may be rolled back.
	Enrolled func(ctx context.Context, root string) (bool, error)
}

// WorktreeEnrolOptions configures the enrolment.
type WorktreeEnrolOptions struct {
	Ports WorktreePorts
	Clock func() time.Time
	// Rename defaults to os.Rename; tests inject failures.
	Rename func(oldPath, newPath string) error
}

// WorktreeEnrolResult reports what the enrolment did.
type WorktreeEnrolResult struct {
	BackupPath string
	AsideDir   string
	// PartialDir is where a failed attempt's half-built store was kept.
	PartialDir string
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
	return &WorktreeEnrolment{o: o}
}

// EnrolWorktree runs the ADR-007 section 4.2 procedure for the worktree at
// r.Path: take exclusive ownership of the board, refuse when local-only events
// exist, back up, write a durable marker, move the store aside, clone fresh with
// the worktree's own secrets, carry the inbox state by event id, enroll, remove
// the marker. Any failure after the move restores the original store. It never
// discards local state: a half-built store is kept aside, never deleted.
func (s *WorktreeEnrolment) EnrolWorktree(ctx context.Context, r EnrollmentRequest) (res *WorktreeEnrolResult, retErr error) {
	root, err := s.validate(r)
	if err != nil {
		return nil, err
	}
	// The lock lives under .mtix/enroll.d, not in .mtix/data: the procedure moves
	// the data directory, and a lock inside it would move with it.
	lock, err := pushlock.AcquireNamed(enrolDir(filepath.Join(root, ".mtix")), enrolLockName)
	if err != nil {
		return nil, fmt.Errorf("lock worktree enrolment: %w", err)
	}
	defer func() {
		if relErr := lock.Release(); relErr != nil {
			retErr = errors.Join(retErr, relErr)
		}
	}()
	return s.run(ctx, root, r)
}

func (s *WorktreeEnrolment) validate(r EnrollmentRequest) (string, error) {
	p := s.o.Ports
	if p.Snapshot == nil || p.HubMissing == nil || p.Backup == nil || p.Clone == nil || p.ImportCarry == nil || p.Enroll == nil || p.Enrolled == nil {
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

func (s *WorktreeEnrolment) p() WorktreePorts { return s.o.Ports }
