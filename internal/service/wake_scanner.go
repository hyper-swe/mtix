// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// harnessPi marks registrations whose wake is claimed by the pi wait child, never by a scan.
const harnessPi = "pi"

// WakeScanStore is the read and mark capability the scanner needs from the board.
type WakeScanStore interface {
	ListRegistrations(ctx context.Context, hostID, uid string) ([]sqlite.Registration, error)
	InboxList(ctx context.Context, agentID string) ([]sqlite.InboxEvent, error)
	MarkRegistrationStale(ctx context.Context, key sqlite.RegistrationKey, epoch int64, since string) (int64, error)
}

// WakeScanDispatcher is the delivery side: the guarded pass and the route check.
type WakeScanDispatcher interface {
	PassSkipping(ctx context.Context, keys []model.WakeKey, skip map[model.WakeKey]bool) error
	HasLiveRoute(ctx context.Context, key model.WakeKey) bool
}

// WakeScannerOptions injects every capability of a scan; all but Logger are required.
type WakeScannerOptions struct {
	Store      WakeScanStore
	Dispatcher WakeScanDispatcher
	Liveness   Liveness
	// Wakeable is the grant check (ADR-007 D3): false or an error means nothing is passed.
	Wakeable func(ctx context.Context, agent string) (bool, string, error)
	// Writable proves the caller can write the operator-local mirror and alert log (A4).
	Writable func() error
	// ExpireStale deletes registrations unseen for 24 hours whose probe reports dead.
	ExpireStale func(ctx context.Context) error
	HostID, UID string
	Clock       func() time.Time
	Logger      *slog.Logger
}

// WakeScanner is the wake pass run by every trigger: the host dispatcher, the CLI and MCP
// post-command callback and the pi wait child's test double (ADR-007 section 2.7, FR-20).
// It enumerates candidates and applies the gates it owns; the dispatcher claims, sends and
// records. The ledger primary key makes concurrent scans safe: one claim per
// (registration, event). It runs on activity only and starts no timer.
type WakeScanner struct {
	opts WakeScannerOptions
}

// NewWakeScanner validates the injected capabilities.
func NewWakeScanner(opts WakeScannerOptions) (*WakeScanner, error) {
	if opts.Store == nil || opts.Dispatcher == nil || opts.Liveness == nil || opts.Wakeable == nil ||
		opts.Writable == nil || opts.ExpireStale == nil || opts.HostID == "" || opts.UID == "" {
		return nil, fmt.Errorf("wake scanner capabilities are required: %w", model.ErrInvalidInput)
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &WakeScanner{opts: opts}, nil
}

// Scan runs steps 0 to 7 of ADR-007 section 2.7 once. A4: when the caller cannot write
// the operator-local state it claims nothing and returns nil, so the host dispatcher is
// the one that wakes. One registration's failure never stops another; errors are joined.
func (s *WakeScanner) Scan(ctx context.Context) error {
	if err := s.opts.Writable(); err != nil {
		s.opts.Logger.Debug("wake scan: operator state not writable; claiming nothing", "error", err)
		return nil
	}
	// No session identity on this context: reading the inbox must not refresh a dead row's last_seen.
	ctx = sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{})
	failures := s.opts.ExpireStale(ctx)
	regs, err := s.opts.Store.ListRegistrations(ctx, s.opts.HostID, s.opts.UID)
	if err != nil {
		return errors.Join(failures, fmt.Errorf("list registrations for wake scan: %w", err))
	}
	for i := range regs {
		if regs[i].Harness == harnessPi {
			continue
		}
		failures = errors.Join(failures, s.scanRegistration(ctx, &regs[i]))
	}
	return failures
}

// Gate reports whether a candidate's registration passes the scanner-owned gates (grant,
// then liveness, then a live route). It is the dispatcher's CandidateFilter, so a held
// wake re-evaluated after a grant change meets the same gates as a scan (ADR-007 2.7).
func (s *WakeScanner) Gate(ctx context.Context, key model.WakeKey) (bool, error) {
	ctx = sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{})
	ok, _, err := s.opts.Wakeable(ctx, key.AgentID)
	if err != nil || !ok {
		return false, err
	}
	regs, err := s.opts.Store.ListRegistrations(ctx, s.opts.HostID, s.opts.UID)
	if err != nil {
		return false, fmt.Errorf("list registrations for wake gate: %w", err)
	}
	for i := range regs {
		if regs[i].AgentID == key.AgentID && regs[i].Epoch == key.Epoch {
			return s.opts.Liveness.Probe(ctx, regs[i]) != LiveDead && s.opts.Dispatcher.HasLiveRoute(ctx, key), nil
		}
	}
	return false, nil
}
