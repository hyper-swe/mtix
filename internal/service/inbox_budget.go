// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeInboxBudget charges inbox returns to the recipient's wake budget mirror,
// the same rolling hourly allowance (wake.wakes_per_hour) the wake dispatcher
// uses, so one limit governs every path that hands an agent new work
// (ADR-007 A12, D4). Without a configured limit nothing is capped.
type WakeInboxBudget struct {
	State  *oplocal.State
	HostID string
	UID    string
	Clock  func() time.Time
}

// Admit charges up to want events in one protected update and returns the
// number granted; it is Locked with a single admit call.
func (b WakeInboxBudget) Admit(ctx context.Context, agent string, want int64) (int64, error) {
	var granted int64
	err := b.Locked(ctx, agent, func(admit AdmitFunc) (admitErr error) {
		granted, admitErr = admit(want)
		return admitErr
	})
	return granted, err
}

// Locked runs fn in the wake dispatcher's own critical section (the protected
// budget mirror lock). admit adds rolling credits only: it does not touch the
// unacked or back-off counters, which belong to the dispatcher.
func (b WakeInboxBudget) Locked(ctx context.Context, agent string, fn func(admit AdmitFunc) error) error {
	limit, err := b.limit(agent)
	if err != nil {
		return err
	}
	if limit == 0 { // D4: no budget configured, nothing is capped
		return fn(func(want int64) (int64, error) { return max(0, want), nil })
	}
	recipient := model.WakeRecipient{AgentID: agent, HostID: b.HostID, UID: b.UID}
	err = b.State.UpdateWakeBudget(ctx, recipient, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		now, nowErr := wakeBudgetNow(b.Clock(), m)
		if nowErr != nil {
			return nowErr
		}
		trimWakeBudgetCredits(m, now)
		return fn(func(want int64) (int64, error) {
			granted := min(max(0, want), max(0, limit-wakeBudgetUsed(m)))
			if granted == 0 {
				return 0, nil
			}
			m.Credits = append(m.Credits, oplocal.WakeBudgetCredit{At: now, Count: granted})
			return granted, save()
		})
	})
	if err != nil {
		return fmt.Errorf("charge inbox budget: %w", err)
	}
	return nil
}

// limit is wake.wakes_per_hour for the agent; 0 means none is configured.
func (b WakeInboxBudget) limit(agent string) (int64, error) {
	values, reports, err := b.State.WakeSettings(agent)
	if err != nil || len(reports) != 0 {
		return 0, fmt.Errorf("read inbox budget policy: %w", model.ErrOperatorStateUnreadable)
	}
	raw := values["wake.wakes_per_hour"].Value
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("inbox budget limit is invalid: %w", model.ErrInvalidInput)
	}
	return limit, nil
}
