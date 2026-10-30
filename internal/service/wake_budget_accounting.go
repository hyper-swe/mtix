// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"math"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func wakeBudgetNow(observation time.Time, m *oplocal.WakeBudgetMirror) (time.Time, error) {
	now := observation.UTC()
	if now.IsZero() || now.Year() < 1 || now.Year() > 9999 {
		return now, fmt.Errorf("wake accounting clock is invalid: %w", model.ErrInvalidInput)
	}
	if m.ObservedAt.After(now) {
		now = m.ObservedAt
	}
	m.ObservedAt = now
	return now, nil
}

func trimWakeBudgetCredits(m *oplocal.WakeBudgetMirror, now time.Time) {
	kept := make([]oplocal.WakeBudgetCredit, 0, len(m.Credits))
	for _, credit := range m.Credits {
		if credit.At.After(now.Add(-time.Hour)) {
			kept = append(kept, credit)
		}
	}
	m.Credits = kept
}

func wakeBudgetUsed(m *oplocal.WakeBudgetMirror) int64 {
	var total int64
	for _, credit := range m.Credits {
		total += credit.Count // The protected codec validates the sum before admission.
	}
	return total
}

func chargeWakeBudget(m *oplocal.WakeBudgetMirror, now time.Time, count int64) error {
	if count < 0 || count > math.MaxInt64-wakeBudgetUsed(m) || count > math.MaxInt64-m.Unacked {
		return fmt.Errorf("wake accounting capacity is exhausted: %w", model.ErrInvalidInput)
	}
	if count == 0 {
		return nil
	}
	m.Credits = append(m.Credits, oplocal.WakeBudgetCredit{At: now, Count: count})
	m.Unacked += count
	m.LastWakeAt = now
	return nil
}

func (a *wakeAdmission) prepareWakeAdmission(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
	m := a.mirror
	row, err := a.reconcileWakeBudget(p)
	d := model.WakeBudgetDecision{Floor: row.Total, BoardID: row.BoardID}
	if err == nil {
		err = checkpointWakeRewind(a, a.save)
	}
	if err != nil {
		return d, fmt.Errorf("prepare wake accounting decision: %w", err)
	}
	if !p.Claimable {
		return d, nil
	}
	if p.Attempt.Token == "" || p.Attempt.Key != a.key {
		return d, fmt.Errorf("proposed wake fence is mismatched: %w", model.ErrConflict)
	}
	if a.holdWakeFrequency(p.Sender) {
		return d, nil
	}
	if holdWakeBackoff(a.key.AgentID, a.policy, a.now, m, a) {
		return d, nil
	}
	if row.Total == math.MaxInt64 {
		return d, fmt.Errorf("protected wake counter is exhausted: %w", model.ErrInvalidInput)
	}
	if err := chargeWakeBudget(m, a.now, 1); err != nil {
		return d, fmt.Errorf("charge proposed wake attempt: %w", err)
	}
	m.Credits[len(m.Credits)-1].Sender = p.Sender
	row.Total++
	row.Pending = p.Attempt.Token
	m.Sources[p.Snapshot.Source] = row
	m.Boards[row.BoardID] = oplocal.WakeBudgetBoard{Total: row.Total, Committed: row.Committed, Pending: row.Pending}
	setWakeBackoffDeadline(a.policy, m)
	// A failed or interrupted SQL commit cannot erase this pending charge.
	if err := a.save(); err != nil {
		return d, fmt.Errorf("persist pending wake acceptance: %w", err)
	}
	d.Allow = true
	return d, nil
}

func checkpointWakeRewind(a *wakeAdmission, save func() error) error {
	if !a.rewound {
		return nil
	}
	// Persist evidence before the claim transaction can heal the store floor,
	// including decisions that hold a wake or lose to an existing claim.
	if err := save(); err != nil {
		return fmt.Errorf("persist wake rewind diagnostic: %w", err)
	}
	return nil
}
