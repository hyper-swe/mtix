// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// HostBoardOptions injects one checkout; no process-wide application or DSN is shared.
type HostBoardOptions struct {
	Store    *sqlite.Store
	State    *oplocal.State
	Path     string
	Host     RegistrationHost
	PID      int64
	Clock    func() time.Time
	Logger   *slog.Logger
	Liveness Liveness
	Adapters []WakeAdapter
	Pull     func(context.Context, string) error
	Hooks    func(context.Context)
	Contact  *ContactScheduler
	Close    func() error
	// LegacyService reports whether a 0.5.x per-project service still owns pull and hook duty for
	// the board's .mtix directory (ADR-007 D11); nil means no legacy probe.
	LegacyService func(ctx context.Context, mtixDir string) (bool, error)
}

// HostBoard composes the real scanner and fenced dispatcher for FR-20.4.
type HostBoard struct {
	opts    HostBoardOptions
	scanner *WakeScanner
	guard   *WakeDispatcher
}

// NewHostBoard constructs a local-only scanner; enrollment is checked on every pass.
func NewHostBoard(o HostBoardOptions) (*HostBoard, error) {
	if o.Store == nil || o.State == nil || o.Path == "" || o.Host.HostID == "" || o.Host.UID == "" || o.PID <= 0 {
		return nil, fmt.Errorf("host board capabilities required: %w", model.ErrInvalidInput)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Liveness == nil {
		o.Liveness = NewProcessLiveness(procinfo.StartTime)
	}
	if len(o.Adapters) == 0 {
		o.Adapters = []WakeAdapter{inboxOnlyAdapter{}}
	}
	b := &HostBoard{opts: o}
	guard, err := NewWakeDispatcher(WakeDispatcherOptions{Store: o.Store, Alerts: NewAlerts(o.State, o.Clock), Adapters: o.Adapters, BudgetState: o.State, Settings: o.State, Clock: o.Clock, HostID: o.Host.HostID, UID: o.Host.UID, CandidateFilter: func(ctx context.Context, k model.WakeKey) (bool, error) { return b.scanner.Gate(ctx, k) }})
	if err != nil {
		return nil, err
	}
	b.guard = guard
	registrations := NewRegistrationService(RegistrationDeps{Store: o.Store, Host: o.Host, Liveness: o.Liveness, Clock: o.Clock})
	b.scanner, err = NewWakeScanner(WakeScannerOptions{Store: o.Store, Dispatcher: guard, Liveness: o.Liveness, HostID: o.Host.HostID, UID: o.Host.UID, Clock: o.Clock, Logger: o.Logger, Writable: o.State.ProbeWritable, Wakeable: b.wakeable, ExpireStale: func(ctx context.Context) error { _, expireErr := registrations.ExpireStale(ctx); return expireErr }})
	if err != nil {
		return nil, err
	}
	if b.opts.Hooks == nil {
		hooks := NewHooksDispatcher(o.Store, filepath.Join(o.Path, ".mtix"), o.Logger)
		hooks.MarkDaemon()
		b.opts.Hooks = hooks.Dispatch
	}
	return b, nil
}

func (b *HostBoard) identity(ctx context.Context) (oplocal.ListedBoard, error) {
	var row oplocal.ListedBoard
	row.Path = b.opts.Path
	if err := oplocal.ValidateLaneLocation(oplocal.LaneLocation{Harness: "dispatcher", Root: row.Path, Path: row.Path}); err != nil {
		return row, err
	}
	for _, path := range []string{filepath.Join(row.Path, ".mtix"), filepath.Join(row.Path, ".mtix", "data")} {
		info, err := os.Lstat(path)
		if err != nil {
			return row, err
		}
		if !info.IsDir() {
			return row, fmt.Errorf("board directory must be real: %w", model.ErrInvalidInput)
		}
	}
	instance, err := b.opts.Store.StoreInstanceID(ctx)
	if err != nil {
		return row, err
	}
	if instance == "" {
		return row, fmt.Errorf("store has no local instance identity: %w", model.ErrInvalidInput)
	}
	row.StoreInstanceID = instance
	row.Key = instance
	ids, err := ReadBoardIDs(filepath.Join(row.Path, ".mtix"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return row, err
	}
	if len(ids) > 1 {
		return row, fmt.Errorf("board identity conflict: %w", model.ErrConflict)
	}
	if len(ids) == 1 {
		row.Key = ids[0]
	}
	return row, nil
}

func (b *HostBoard) enrolled(ctx context.Context, row oplocal.ListedBoard) (bool, error) {
	registry, err := b.opts.State.DispatchBoards(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range registry.Enrolled {
		if entry.Path == row.Path && entry.BoardID == row.Key && entry.StoreInstanceID == row.StoreInstanceID {
			return true, nil
		}
	}
	return false, nil
}

func (b *HostBoard) wakeable(ctx context.Context, agent string) (bool, string, error) {
	row, err := b.identity(ctx)
	if err != nil {
		return false, "board identity unavailable", err
	}
	evidence := HubEvidence{}
	if _, secretErr := os.Lstat(filepath.Join(row.Path, ".mtix", "secrets")); secretErr == nil {
		evidence.SecretsDSN = true
	} else if !errors.Is(secretErr, os.ErrNotExist) {
		return false, "secrets unavailable", secretErr
	}
	status, err := NewHubMarkService(b.opts.Store).Inspect(ctx, evidence)
	if err != nil {
		return false, "hub status unavailable", err
	}
	if status.Connected {
		ok, enrollmentErr := b.enrolled(ctx, row)
		if enrollmentErr != nil || !ok {
			return false, "checkout unenrolled", enrollmentErr
		}
	}
	observation, reports, err := b.opts.State.BoardObservation(row.Path)
	if err != nil || len(reports) > 0 {
		return false, "operator state unreadable", fmt.Errorf("board observation unavailable: %w", model.ErrOperatorStateUnreadable)
	}
	permissions := NewWakePermissions(PermissionDeps{Store: b.opts.Store, State: b.opts.State, HostID: b.opts.Host.HostID, UID: b.opts.Host.UID, Clock: b.opts.Clock})
	return permissions.Wakeable(ctx, PermissionBoard{ID: row.Key, Checkout: row.Path, Hub: observation.Hub, Evidence: evidence}, agent)
}

func (b *HostBoard) pass(ctx context.Context, listed oplocal.ListedBoard) (retErr error) {
	if b.opts.Close != nil {
		defer func() { retErr = errors.Join(retErr, b.opts.Close()) }()
	}
	row, err := b.identity(ctx)
	if err != nil {
		return err
	}
	if row != listed && listed.Path == row.Path && listed.Key == listed.StoreInstanceID && listed.StoreInstanceID == row.StoreInstanceID {
		if err := b.opts.State.ListBoardInstance(ctx, row); err != nil {
			return err
		}
		listed = row
	}
	if row != listed {
		return fmt.Errorf("listed checkout identity or instance changed: %w", model.ErrConflict)
	}
	routing := &AttachRouting{State: b.opts.State, Board: AttachBoard{Key: row.Key, Path: row.Path}, LanePath: row.Path, StoreInstanceID: row.StoreInstanceID}
	registrations := NewRegistrationService(RegistrationDeps{Store: b.opts.Store, Host: b.opts.Host, Clock: b.opts.Clock, Routing: routing})
	_, drainErr := registrations.DrainAttachPending(ctx, routing.Board)
	beatErr := b.opts.Store.RecordDispatcherHeartbeat(ctx, sqlite.DispatcherHeartbeat{HostID: b.opts.Host.HostID, UID: b.opts.Host.UID, PID: b.opts.PID, Version: b.opts.Host.Version, Protocol: b.opts.Host.Protocol, SeenAt: b.opts.Clock().UTC().Format(time.RFC3339Nano)})
	effectsErr := b.effects(ctx, row)
	// Check even on an empty candidate set: Scan itself has no pass when no registrations exist.
	guardErr := b.guard.Pass(ctx, nil)
	var scanErr error
	if guardErr == nil {
		scanErr = b.scanner.Scan(ctx)
	}
	return errors.Join(drainErr, beatErr, effectsErr, guardErr, scanErr)
}

func (b *HostBoard) effects(ctx context.Context, row oplocal.ListedBoard) (retErr error) {
	enrolled, err := b.enrolled(ctx, row)
	if err != nil || !enrolled {
		return err
	}
	lock, err := pushlock.AcquireNamed(filepath.Join(row.Path, ".mtix"), "dispatch.lock")
	if errors.Is(err, pushlock.ErrLockHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	enrolled, err = b.enrolled(ctx, row)
	if err != nil || !enrolled {
		return err
	}
	// A live 0.5.x per-project service keeps pull and hook duty until setup migrates it (D11).
	if legacy, err := b.legacyLive(ctx, row); err != nil || legacy {
		return err
	}
	if b.opts.Contact != nil && b.opts.Pull != nil && b.opts.Contact.Decide().Contact {
		dsn, err := readCheckoutSecret(filepath.Join(row.Path, ".mtix"))
		if err != nil {
			return err
		}
		if dsn != "" {
			b.opts.Contact.MarkContact()
			retErr = b.opts.Pull(ctx, dsn)
		}
	}
	b.opts.Hooks(ctx)
	return retErr
}

// legacyLive reports whether a 0.5.x service still owns this board's pull and hook duty.
// It only reads; the host dispatcher never writes the legacy PID file (ADR-007 D11).
// A probe error fails closed: the caller skips effects and surfaces the error.
func (b *HostBoard) legacyLive(ctx context.Context, row oplocal.ListedBoard) (bool, error) {
	if b.opts.LegacyService == nil {
		return false, nil
	}
	live, err := b.opts.LegacyService(ctx, filepath.Join(row.Path, ".mtix"))
	if err != nil {
		return false, fmt.Errorf("legacy service probe: %w", err)
	}
	return live, nil
}
