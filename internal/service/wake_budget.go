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
		return nil, fmt.Errorf("injected wake accounting clock is required: %w", model.ErrInvalidInput)
	}
	return &wakeBudgets{store: store, state: state, settings: settings, alerts: opts.Alerts, clock: clock, held: opts.Held}, nil
}

type wakeAdmission struct {
	key     model.WakeKey
	policy  wakeBudgetPolicy
	now     time.Time
	mirror  *oplocal.WakeBudgetMirror
	save    func() error
	result  model.WakeBudgetResult
	held    string
	fix     string
	class   string
	rewound bool
	// returned: an inbox read already returned and charged this event.
	returned bool
	// claim writes the fenced claim once admission allows it: the ordinary claim, or the
	// reopen of a stalled sent event (MTIX-112.40.12). Both share this one admission.
	claim func(func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error)) (model.WakeBudgetResult, error)
}

func (b *wakeBudgets) claim(ctx context.Context, key model.WakeKey) (model.WakeAttempt, bool, error) {
	attempt, won, _, err := b.claimState(ctx, key)
	return attempt, won, err
}

// wakeClaimState says why a claim was not won.
type wakeClaimState int

const (
	claimSettled    wakeClaimState = iota // won, or the ledger row is terminal
	claimBudgetHeld                       // the recipient budget or back-off refused the claim
	claimInFlight                         // another claimant holds an unexpired unfinished claim
	claimReplaced                         // the snapshotted event was removed or replaced
)

// claimState also reports whether the budget held the wake or another claim is in flight.
func (b *wakeBudgets) claimState(ctx context.Context, key model.WakeKey) (model.WakeAttempt, bool, wakeClaimState, error) {
	return b.admitVia(ctx, key, func(admit WakeAdmitFunc) (model.WakeBudgetResult, error) {
		return b.store.ClaimWakeBudget(ctx, key, time.Minute, admit)
	})
}

// admitStalled is the StalledAdmission of the dispatcher: the reopen of a stalled sent event
// goes through the same budget and back-off admission as a first claim.
func (b *wakeBudgets) admitStalled(ctx context.Context, key model.WakeKey, reopen func(WakeAdmitFunc) (model.WakeBudgetResult, error)) (model.WakeAttempt, bool, error) {
	attempt, won, _, err := b.admitVia(ctx, key, reopen)
	return attempt, won, err
}

// admitVia applies the recipient's wake budget and back-off policy to a claim that claim
// writes (see wakeAdmission.claim) and reports why it was not won.
func (b *wakeBudgets) admitVia(ctx context.Context, key model.WakeKey, claim func(WakeAdmitFunc) (model.WakeBudgetResult, error)) (model.WakeAttempt, bool, wakeClaimState, error) {
	policy, err := b.policy(key.AgentID)
	if err != nil {
		return model.WakeAttempt{}, false, claimSettled, fmt.Errorf("load wake admission policy: %w", err)
	}
	admission := wakeAdmission{key: key, policy: policy, claim: claim}
	err = b.state.UpdateWakeBudget(ctx, key.Recipient(), func(m *oplocal.WakeBudgetMirror, save func() error) error {
		admission.mirror, admission.save = m, save
		if claimErr := b.claimLocked(ctx, &admission); claimErr != nil {
			return fmt.Errorf("apply protected wake admission: %w", claimErr)
		}
		return nil
	})
	alertErr := b.reportAdmission(ctx, key.AgentID, admission)
	if err != nil || alertErr != nil {
		return model.WakeAttempt{}, false, claimSettled, fmt.Errorf("admit protected wake attempt: %w", errors.Join(err, alertErr))
	}
	state := claimSettled
	switch {
	case admission.result.Replaced:
		state = claimReplaced
	case admission.held != "":
		state = claimBudgetHeld
	case admission.result.InFlight:
		state = claimInFlight
	}
	return admission.result.Attempt, admission.result.Won, state, nil
}

func (b *wakeBudgets) claimLocked(ctx context.Context, a *wakeAdmission) error {
	now, err := wakeBudgetNow(b.clock(), a.mirror)
	if err != nil {
		return fmt.Errorf("observe wake admission time: %w", err)
	}
	a.now = now
	trimWakeBudgetCredits(a.mirror, now)
	if returnedErr := b.noteReturned(ctx, a); returnedErr != nil {
		return returnedErr
	}
	a.result, err = a.claim(a.prepareWakeAdmission)
	if err != nil {
		return fmt.Errorf("persist accounted wake claim: %w", err)
	}
	if a.result.Replaced {
		return nil
	}
	if a.result.Won && (a.result.Attempt.Token == "" || a.result.Attempt.Key != a.key) {
		return fmt.Errorf("accounted wake attempt fence is mismatched: %w", model.ErrConflict)
	}
	row := a.mirror.Sources[a.result.Snapshot.Source]
	if a.result.Won && row.Pending != a.result.Attempt.Token {
		return fmt.Errorf("wake fence has no matching protected admission: %w", model.ErrConflict)
	}
	if a.result.Snapshot.Total != row.Total {
		return fmt.Errorf("accepted wake counter differs from protected intent: %w", model.ErrConflict)
	}
	row.Committed, row.Pending = row.Total, ""
	a.mirror.Sources[a.result.Snapshot.Source] = row
	a.mirror.Boards[row.BoardID] = oplocal.WakeBudgetBoard{Total: row.Total, Committed: row.Total}
	if err := a.save(); err != nil {
		return fmt.Errorf("persist committed wake accounting: %w", err)
	}
	return nil
}

// noteReturned records whether an inbox read already returned (and charged)
// this event, so the wake that follows does not charge it a second time. It
// runs inside the same budget lock the read charges in (ADR-007 A12).
func (b *wakeBudgets) noteReturned(ctx context.Context, a *wakeAdmission) error {
	marks, ok := b.store.(InboxMarkStore)
	if !ok {
		return nil
	}
	mark, err := marks.InboxReturnedMark(ctx, a.key.AgentID)
	if err != nil {
		return fmt.Errorf("read inbox returned mark: %w", err)
	}
	a.returned = a.key.EventSeq <= mark
	return nil
}
