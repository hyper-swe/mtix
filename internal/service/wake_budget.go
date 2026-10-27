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

// WakeBudgetStore atomically applies policy to a proposed fenced local claim.
type WakeBudgetStore interface {
	ClaimWakeBudget(context.Context, model.WakeKey, time.Duration,
		func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error)) (model.WakeBudgetResult, error)
}

type wakeBudgets struct {
	store    WakeBudgetStore
	state    *oplocal.State
	settings WakeSettingsReader
	alerts   WakeAlertWriter
	clock    func() time.Time
	held     func(string)
}

func newWakeBudgets(opts WakeDispatcherOptions) (*wakeBudgets, error) {
	store, ok := opts.Store.(WakeBudgetStore)
	if !ok {
		return nil, fmt.Errorf("durable wake accounting capability is required: %w", model.ErrInvalidInput)
	}
	state := opts.BudgetState
	if state == nil {
		return nil, fmt.Errorf("protected wake accounting state is required: %w", model.ErrInvalidInput)
	}
	settings := opts.Settings
	if settings == nil {
		settings = state
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &wakeBudgets{store: store, state: state, settings: settings, alerts: opts.Alerts, clock: clock, held: opts.Held}, nil
}

type wakeAdmission struct {
	result  model.WakeBudgetResult
	held    string
	fix     string
	class   string
	rewound bool
}

func (b *wakeBudgets) claim(ctx context.Context, key model.WakeKey) (model.WakeAttempt, bool, error) {
	policy, err := b.policy(key.AgentID)
	if err != nil {
		return model.WakeAttempt{}, false, err
	}
	var admission wakeAdmission
	err = b.state.UpdateWakeBudget(ctx, key.Recipient(), func(m *oplocal.WakeBudgetMirror, save func() error) error {
		return b.claimLocked(ctx, key, policy, m, save, &admission)
	})
	alertErr := b.reportAdmission(ctx, key.AgentID, admission)
	if err != nil || alertErr != nil {
		return model.WakeAttempt{}, false, errors.Join(err, alertErr)
	}
	return admission.result.Attempt, admission.result.Won, nil
}

func (b *wakeBudgets) claimLocked(ctx context.Context, key model.WakeKey, policy wakeBudgetPolicy,
	m *oplocal.WakeBudgetMirror, save func() error, a *wakeAdmission,
) error {
	now, err := wakeBudgetNow(b.clock(), m)
	if err != nil {
		return err
	}
	trimWakeBudgetCredits(m, now)
	a.result, err = b.store.ClaimWakeBudget(ctx, key, time.Minute, func(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return prepareWakeAdmission(key, p, policy, now, m, save, a)
	})
	if err != nil {
		return fmt.Errorf("persist accounted wake claim: %w", err)
	}
	if a.result.Won && (a.result.Attempt.Token == "" || a.result.Attempt.Key != key) {
		return fmt.Errorf("accounted wake attempt fence is mismatched: %w", model.ErrConflict)
	}
	row := m.Sources[a.result.Snapshot.Source]
	if a.result.Won && row.Pending != a.result.Attempt.Token {
		return fmt.Errorf("wake fence has no matching protected admission: %w", model.ErrConflict)
	}
	if a.result.Snapshot.Total != row.Total {
		return fmt.Errorf("accepted wake counter differs from protected intent: %w", model.ErrConflict)
	}
	row.Committed, row.Pending = row.Total, ""
	m.Sources[a.result.Snapshot.Source] = row
	return save()
}
