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

func reconcileWakeBudget(m *oplocal.WakeBudgetMirror, snap model.WakeBudgetSnapshot,
	now time.Time, a *wakeAdmission,
) (oplocal.WakeBudgetSource, error) {
	if snap.Source == "" || snap.Total < 0 || snap.AckMark < 0 {
		return oplocal.WakeBudgetSource{}, fmt.Errorf("wake accounting snapshot is invalid: %w", model.ErrInvalidInput)
	}
	row, known := m.Sources[snap.Source]
	if !known {
		// A moved board's lifetime counter is a baseline, not new attempts.
		row.Total, row.Committed = snap.Total, snap.Total
	}
	if known && snap.Total < row.Committed {
		if m.Rewinds == math.MaxUint64 {
			return row, fmt.Errorf("wake rewind counter is exhausted: %w", model.ErrInvalidInput)
		}
		m.Rewinds++
		m.LastRewindAt, a.rewound = now, true
	}
	row.AckMark = max(row.AckMark, snap.AckMark)
	if snap.Total > row.Total {
		if err := chargeWakeBudget(m, now, snap.Total-row.Total); err != nil {
			return row, err
		}
		row.Total = snap.Total
	}
	if snap.Total >= row.Total {
		row.Committed, row.Pending = row.Total, ""
	}
	m.Sources[snap.Source] = row
	return row, nil
}

func (a *wakeAdmission) prepareWakeAdmission(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
	m := a.mirror
	row, err := reconcileWakeBudget(m, p.Snapshot, a.now, a)
	d := model.WakeBudgetDecision{Floor: row.Total}
	if err == nil {
		err = checkpointWakeRewind(a, a.save)
	}
	if err != nil || !p.Claimable {
		return d, err
	}
	if p.Attempt.Token == "" || p.Attempt.Key != a.key {
		return d, fmt.Errorf("proposed wake fence is mismatched: %w", model.ErrConflict)
	}
	if a.policy.limit > 0 && wakeBudgetUsed(m) >= a.policy.limit {
		a.class = "wake.budget"
		a.fix = wakeBudgetFix(a.key.AgentID, a.policy.limit)
		a.held = fmt.Sprintf("held wake: recipient rolling-hour budget %d is spent; %s", a.policy.limit, a.fix)
		return d, nil
	}
	if holdWakeBackoff(a.key.AgentID, a.policy, a.now, m, a) {
		return d, nil
	}
	if row.Total == math.MaxInt64 {
		return d, fmt.Errorf("protected wake counter is exhausted: %w", model.ErrInvalidInput)
	}
	if err := chargeWakeBudget(m, a.now, 1); err != nil {
		return d, err
	}
	row.Total++
	row.Pending = p.Attempt.Token
	m.Sources[p.Snapshot.Source] = row
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
