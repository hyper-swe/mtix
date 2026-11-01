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
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// HostEnrollmentNotice is the identifying-free board activity line (D10).
const HostEnrollmentNotice = "dispatcher enrollment changed on a host (see that host's mtix doctor)"

// EnrollmentRequest comes from an explicit operator action, never registration or routing.
type EnrollmentRequest struct {
	Path, Mode, Source string
	// MigrateLegacy removes the board's 0.5.x per-project service before enrolling (ADR-007 D11).
	MigrateLegacy bool
}

// HostEnrollmentOptions supplies checkout opening and observation using that checkout's secrets.
type HostEnrollmentOptions struct {
	State      *oplocal.State
	Clock      func() time.Time
	OpenBoard  func(context.Context, string) (*HostBoard, error)
	ObserveHub func(context.Context, string) (oplocal.HubIdentity, error)
	// UninstallLegacy stops and removes the per-project service of the checkout root.
	UninstallLegacy func(ctx context.Context, root string) error
	// LegacyLive reports whether a legacy daemon still runs for the board's .mtix directory.
	LegacyLive func(ctx context.Context, mtixDir string) (bool, error)
}

// HostEnrollment is the service-only surface consumed by setup; it creates no agent grants.
type HostEnrollment struct{ opts HostEnrollmentOptions }

// NewHostEnrollment constructs the enrollment service (FR-20.4, D10).
func NewHostEnrollment(o HostEnrollmentOptions) *HostEnrollment {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &HostEnrollment{opts: o}
}

// List reads enrolled checkout authority without contacting any hub.
func (s *HostEnrollment) List(ctx context.Context) ([]oplocal.EnrolledBoard, error) {
	if s.opts.State == nil {
		return nil, fmt.Errorf("enrollment state required: %w", model.ErrInvalidInput)
	}
	b, err := s.opts.State.DispatchBoards(ctx)
	return b.Enrolled, err
}

// Enroll requires an explicit mode and independently observes the hub with checkout-only secrets.
func (s *HostEnrollment) Enroll(ctx context.Context, r EnrollmentRequest) error {
	return s.change(ctx, r, true)
}

// MigrateLegacyService replaces a board's 0.5.x per-project service with enrollment (ADR-007 D11, FR-20.4).
func (s *HostEnrollment) MigrateLegacyService(ctx context.Context, r EnrollmentRequest) error {
	r.MigrateLegacy = true
	return s.Enroll(ctx, r)
}

// Unenroll revokes only this checkout after any in-flight locked pass completes.
func (s *HostEnrollment) Unenroll(ctx context.Context, r EnrollmentRequest) error {
	return s.change(ctx, r, false)
}

func (s *HostEnrollment) change(ctx context.Context, r EnrollmentRequest, enroll bool) (retErr error) {
	if s.opts.State == nil || s.opts.OpenBoard == nil || r.Source == "" || (r.Mode != "interactive" && r.Mode != "yes" && r.Mode != "file") {
		return fmt.Errorf("explicit enrollment action required: %w", model.ErrInvalidInput)
	}
	canonical, err := canonicalEnrollmentPath(r.Path)
	if err != nil {
		return err
	}
	b, err := s.opts.OpenBoard(ctx, canonical)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("enrollment board unavailable: %w", model.ErrInvalidInput)
	}
	if b.opts.Close != nil {
		defer func() { retErr = errors.Join(retErr, b.opts.Close()) }()
	}
	row, err := b.identity(ctx)
	if err != nil {
		return err
	}
	if row.Path != canonical {
		return fmt.Errorf("enrollment opener returned another checkout: %w", model.ErrInvalidInput)
	}
	lock, err := pushlock.AcquireNamed(filepath.Join(canonical, ".mtix"), "dispatch.lock")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	return s.apply(ctx, canonical, b, row, r, enroll)
}

// enrollmentPlan is what the preflight learned before any state is touched.
type enrollmentPlan struct {
	enroll bool
	hub    oplocal.HubIdentity
}

// apply runs every fallible precondition (hub observation, checkout secret, migration hooks)
// before the legacy service is removed, so a refusal never leaves the board without an owner
// of pull and hook duty (ADR-007 D11). The dispatch lock is already held by the caller.
func (s *HostEnrollment) apply(ctx context.Context, root string, b *HostBoard, row oplocal.ListedBoard, r EnrollmentRequest, enroll bool) error {
	plan := enrollmentPlan{enroll: enroll}
	migrate := enroll && r.MigrateLegacy
	if migrate && (s.opts.UninstallLegacy == nil || s.opts.LegacyLive == nil) {
		return fmt.Errorf("legacy service migration requires an uninstaller and a liveness probe: %w", model.ErrInvalidInput)
	}
	if enroll {
		hub, err := s.observe(ctx, b)
		if err != nil {
			return err
		}
		plan.hub = hub
	}
	if migrate {
		if err := s.migrateLegacy(ctx, root); err != nil {
			return err
		}
	}
	if err := s.completeChange(ctx, b, row, r, plan); err != nil {
		if migrate {
			// Only local state writes remain after the removal; name the residual so the operator re-runs.
			return fmt.Errorf("legacy service removed but enrollment did not complete; re-run the migration: %w", err)
		}
		return err
	}
	return nil
}

// migrateLegacy removes the per-project service under the dispatch lock, so no host pass
// overlaps it, and refuses to enroll while a legacy daemon still runs (D11). Callers run it
// only after the enrollment preconditions passed.
func (s *HostEnrollment) migrateLegacy(ctx context.Context, root string) error {
	if err := s.opts.UninstallLegacy(ctx, root); err != nil {
		return fmt.Errorf("uninstall legacy service: %w", err)
	}
	live, err := s.opts.LegacyLive(ctx, filepath.Join(root, ".mtix"))
	if err != nil {
		return fmt.Errorf("verify legacy service stopped: %w", err)
	}
	if live {
		return fmt.Errorf("legacy daemon still running; stop it and re-run the migration: %w", model.ErrConflict)
	}
	return nil
}

func (s *HostEnrollment) completeChange(ctx context.Context, b *HostBoard, row oplocal.ListedBoard, r EnrollmentRequest, plan enrollmentPlan) error {
	var entry *oplocal.EnrolledBoard
	action := "unenroll"
	if plan.enroll {
		hub := plan.hub
		if err := s.opts.State.ListBoardInstance(ctx, row); err != nil {
			return err
		}
		entry = &oplocal.EnrolledBoard{Path: row.Path, BoardID: row.Key, StoreInstanceID: row.StoreInstanceID, Hub: hub}
		if err := s.opts.State.ObserveBoard(row.Path, row.Key, hub); err != nil {
			return err
		}
		action = "enroll"
	}
	// Publish before granting authority: failure cannot leave an unaudited enrollment.
	if err := publishEnrollment(ctx, b, s.opts.Clock()); err != nil {
		return err
	}
	audit := oplocal.EnrollmentAudit{Path: row.Path, Action: action, Mode: r.Mode, Source: r.Source, At: s.opts.Clock().UTC()}
	return s.opts.State.SetEnrollment(ctx, entry, audit)
}

func (s *HostEnrollment) observe(ctx context.Context, b *HostBoard) (oplocal.HubIdentity, error) {
	dsn, err := readCheckoutSecret(filepath.Join(b.opts.Path, ".mtix"))
	if err != nil {
		return oplocal.HubIdentity{}, err
	}
	if dsn == "" {
		status, statusErr := NewHubMarkService(b.opts.Store).Inspect(ctx, HubEvidence{})
		if statusErr != nil {
			return oplocal.HubIdentity{}, statusErr
		}
		if status.Connected {
			return oplocal.HubIdentity{}, fmt.Errorf("hub enrollment requires checkout secrets: %w", model.ErrInvalidInput)
		}
		return oplocal.HubIdentity{}, nil
	}
	if s.opts.ObserveHub == nil {
		return oplocal.HubIdentity{}, fmt.Errorf("hub observation required: %w", model.ErrInvalidInput)
	}
	hub, err := s.opts.ObserveHub(ctx, dsn)
	if err != nil {
		return oplocal.HubIdentity{}, fmt.Errorf("observe checkout hub: %w", err)
	}
	if !hub.Known() {
		return hub, fmt.Errorf("hub identity unavailable: %w", model.ErrInvalidInput)
	}
	return hub, nil
}

func publishEnrollment(ctx context.Context, b *HostBoard, at time.Time) error {
	nodes, _, err := b.opts.Store.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: -1})
	if err != nil {
		return err
	}
	var roots []string
	for _, n := range nodes {
		if n.ParentID == "" && n.DeletedAt == nil {
			roots = append(roots, n.ID)
		}
	}
	sort.Strings(roots)
	if len(roots) == 0 {
		// An empty board has no annotation target; the operator-local audit still records the action.
		return nil
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	return b.opts.Store.AppendAnnotationOnce(ctx, roots[0], model.Annotation{ID: id.String(), Author: "mtix-dispatcher", Text: HostEnrollmentNotice, CreatedAt: at.UTC()})
}

// readCheckoutSecret never reads environment or follows a secrets symlink.
func readCheckoutSecret(mtixDir string) (value string, retErr error) {
	path := filepath.Join(mtixDir, "secrets")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 65536 {
		return "", fmt.Errorf("checkout secrets must be a real 0600 file: %w", model.ErrInvalidInput)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return "", fmt.Errorf("checkout secrets changed during open: %w", model.ErrInvalidInput)
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return "", err
	}
	if len(raw) > 65536 {
		return "", fmt.Errorf("checkout secrets too large: %w", model.ErrInvalidInput)
	}
	return strings.TrimSpace(string(raw)), nil
}

func canonicalEnrollmentPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}
