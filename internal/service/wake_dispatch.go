// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeStore is the fenced delivery capability needed by a guarded wake pass.
type WakeStore interface {
	MinDispatchProtocol(context.Context) (int, error)
	ClaimWake(context.Context, model.WakeKey, time.Duration) (model.WakeAttempt, bool, error)
	RecordWakeOutcome(context.Context, model.WakeAttempt, model.WakeOutcome) error
}

// WakeAlertWriter records protocol refusals in operator-local diagnostics.
type WakeAlertWriter interface {
	Raise(context.Context, AlertRequest) (oplocal.Alert, error)
}

// WakeDispatcherOptions supplies already-selected candidates' delivery capabilities.
// Candidate discovery, enrollment and host-dispatcher wiring belong to their own features.
type WakeDispatcherOptions struct {
	Store      WakeStore
	Alerts     WakeAlertWriter
	Send       func(context.Context, model.WakeAttempt) error
	AlertAgent string
}

// WakeDispatcher guards each callable pass before issuing any fenced send (ADR-007 §6.4).
type WakeDispatcher struct {
	store      WakeStore
	alerts     WakeAlertWriter
	send       func(context.Context, model.WakeAttempt) error
	alertAgent string
}

// NewWakeDispatcher requires storage, delivery and durable alert capabilities.
func NewWakeDispatcher(opts WakeDispatcherOptions) (*WakeDispatcher, error) {
	if opts.Store == nil || opts.Alerts == nil || opts.Send == nil {
		return nil, fmt.Errorf("wake dispatcher capabilities are required: %w", model.ErrInvalidInput)
	}
	if opts.AlertAgent == "" {
		opts.AlertAgent = "dispatcher"
	}
	if err := validateAgentID(opts.AlertAgent); err != nil {
		return nil, fmt.Errorf("validate wake diagnostic agent: %w", err)
	}
	return &WakeDispatcher{store: opts.Store, alerts: opts.Alerts, send: opts.Send, alertAgent: opts.AlertAgent}, nil
}

// Pass re-reads the protocol floor even when there are no candidates.
// Each attempted send is terminal; an independent candidate still runs after a send error.
func (d *WakeDispatcher) Pass(ctx context.Context, keys []model.WakeKey) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start wake pass: %w", err)
	}
	if err := d.checkWakeProtocol(ctx); err != nil {
		return err
	}
	for _, key := range keys {
		if err := validateWakeCandidate(key); err != nil {
			return err
		}
	}
	var failures error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, fmt.Errorf("continue wake pass: %w", err))
		}
		failures = errors.Join(failures, d.sendWake(ctx, key))
	}
	return failures
}

func validateWakeCandidate(key model.WakeKey) error {
	if err := validateAgentID(key.AgentID); err != nil {
		return fmt.Errorf("validate wake candidate: %w", err)
	}
	if key.AgentID == "" || key.HostID == "" || key.UID == "" || key.EventSeq <= 0 || key.Epoch < 0 {
		return fmt.Errorf("invalid wake candidate key: %w", model.ErrInvalidInput)
	}
	return nil
}
