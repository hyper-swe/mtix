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
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeStore is the fenced delivery capability needed by a guarded wake pass.
type WakeStore interface {
	MinDispatchProtocol(context.Context) (int, error)
	WakeEventHop(context.Context, model.WakeKey) (int64, error)
	ClaimWake(context.Context, model.WakeKey, time.Duration) (model.WakeAttempt, bool, error)
	RecordWakeOutcome(context.Context, model.WakeAttempt, model.WakeOutcome) error
}

// WakeAlertWriter records protocol refusals in operator-local diagnostics.
type WakeAlertWriter interface {
	Raise(context.Context, AlertRequest) (oplocal.Alert, error)
}

// WakeDispatcherOptions supplies already-selected candidates' delivery capabilities.
// Candidate discovery, enrollment and host-dispatcher wiring belong to their own features.
// The candidate source must follow the Pass contract: every unacknowledged addressed event
// above each registration's mark, so held events are never omitted. The held event is
// derived from the inbox, so the candidate set must equal the inbox set above the mark
// (an event the inbox lists but the candidates omit stalls a held registration until acked).
type WakeDispatcherOptions struct {
	Store  WakeStore
	Alerts WakeAlertWriter
	Send   func(context.Context, model.WakeAttempt) error
	// Adapters are the delivery routes, selected per registration in route order.
	// Send, when set, joins them as the legacy route. At least one of the two is required.
	Adapters    []WakeAdapter
	AlertAgent  string
	Settings    WakeSettingsReader
	Held        func(string)
	BudgetState *oplocal.State
	Clock       func() time.Time
	// HostID and UID scope re-evaluation to this host's and operating-system user's
	// registrations (I31). Required by Reevaluate; Pass ignores them.
	HostID, UID string
	// CandidateFilter applies the gates the wake scanner owns (grant or wakeable and
	// registration liveness, ADR-007 section 2.7). Required by Reevaluate, which fails
	// closed without it; it is called once per held registration.
	CandidateFilter func(context.Context, model.WakeKey) (bool, error)
}

// WakeDispatcher guards each callable pass before issuing any fenced send (ADR-007 §6.4).
type WakeDispatcher struct {
	store      WakeStore
	alerts     WakeAlertWriter
	adapters   []WakeAdapter
	alertAgent string
	settings   WakeSettingsReader
	held       func(string)
	budgets    *wakeBudgets
	holds      WakeHoldStore
	reeval     reevaluationScope
}

// NewWakeDispatcher requires storage, delivery and durable alert capabilities.
func NewWakeDispatcher(opts WakeDispatcherOptions) (*WakeDispatcher, error) {
	if opts.Store == nil || opts.Alerts == nil || (opts.Send == nil && len(opts.Adapters) == 0) {
		return nil, fmt.Errorf("wake dispatcher capabilities are required: %w", model.ErrInvalidInput)
	}
	if opts.AlertAgent == "" {
		opts.AlertAgent = "dispatcher"
	}
	if err := validateAgentID(opts.AlertAgent); err != nil {
		return nil, fmt.Errorf("validate wake diagnostic agent: %w", err)
	}
	if opts.Held == nil {
		opts.Held = func(line string) { slog.Warn(line) }
	}
	budgets, err := newWakeBudgets(opts)
	if err != nil {
		return nil, fmt.Errorf("construct wake accounting: %w", err)
	}
	adapters := append([]WakeAdapter(nil), opts.Adapters...)
	if opts.Send != nil {
		adapters = append(adapters, legacyAdapter{send: opts.Send})
	}
	return &WakeDispatcher{store: opts.Store, alerts: opts.Alerts, adapters: orderAdapters(adapters), alertAgent: opts.AlertAgent, settings: opts.Settings, held: opts.Held, budgets: budgets, holds: holdStore(opts.Store), reeval: reevaluationScope{host: opts.HostID, uid: opts.UID, filter: opts.CandidateFilter}}, nil
}

func holdStore(store WakeStore) WakeHoldStore {
	holds, _ := store.(WakeHoldStore)
	return holds
}

// Pass re-reads the protocol floor even when there are no candidates.
// Every pass re-evaluates held registrations from their own high-water mark (FR-20.3, I10).
// Each attempted send is terminal; an independent candidate still runs after a send error.
//
// Contract (ADR-007 section 2.7): the caller passes every unacknowledged addressed event
// above each registration's high-water mark. A candidate above a registration's pending hold
// while the held lower event is not handled in the same pass is refused with
// model.ErrWakeHeldEventMissing, as is any candidate when the held event cannot be read:
// nothing is sent and the mark and hold stay. If a concurrent pass records the hold after
// this check, the higher event may already be sent, but the store refuses the advance in its
// own transaction, so the mark and hold stay and the wake ledger prevents a second wake.
// A pass that omits a held event can therefore never advance the mark past it.
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
	st := &passState{blocked: map[string]bool{}}
	for _, key := range orderedCandidates(keys) {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, fmt.Errorf("continue wake pass: %w", err))
		}
		failures = errors.Join(failures, d.passOne(ctx, key, st))
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
