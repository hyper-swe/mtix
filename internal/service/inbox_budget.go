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
// number granted. It adds rolling credits only: it does not touch the unacked
// or back-off counters, which belong to the wake dispatcher.
func (b WakeInboxBudget) Admit(ctx context.Context, agent string, want int64) (int64, error) {
	if want <= 0 {
		return 0, nil
	}
	values, reports, err := b.State.WakeSettings(agent)
	if err != nil || len(reports) != 0 {
		return 0, fmt.Errorf("read inbox budget policy: %w", model.ErrOperatorStateUnreadable)
	}
	raw := values["wake.wakes_per_hour"].Value
	if raw == "" {
		return want, nil // D4: no budget configured, nothing is capped
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("inbox budget limit is invalid: %w", model.ErrInvalidInput)
	}
	var granted int64
	recipient := model.WakeRecipient{AgentID: agent, HostID: b.HostID, UID: b.UID}
	err = b.State.UpdateWakeBudget(ctx, recipient, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		now, nowErr := wakeBudgetNow(b.Clock(), m)
		if nowErr != nil {
			return nowErr
		}
		trimWakeBudgetCredits(m, now)
		granted = min(want, max(0, limit-wakeBudgetUsed(m)))
		if granted == 0 {
			return nil
		}
		m.Credits = append(m.Credits, oplocal.WakeBudgetCredit{At: now, Count: granted})
		return save()
	})
	if err != nil {
		return 0, fmt.Errorf("charge inbox budget: %w", err)
	}
	return granted, nil
}
