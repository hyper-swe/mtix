// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func wakeSenderLimit(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("wake sender policy is invalid: %w", model.ErrInvalidInput)
	}
	return limit, nil
}

func wakeSenderUsed(m *oplocal.WakeBudgetMirror, sender string) int64 {
	var total int64
	for _, credit := range m.Credits {
		// Unknown attribution must not refund historical or conservatively recovered debt.
		if sender == "" || credit.Sender == "" || credit.Sender == sender {
			total += credit.Count
		}
	}
	return total
}

func (a *wakeAdmission) holdWakeFrequency(sender string) bool {
	key, label, limit := "wake.wakes_per_hour", "recipient", a.policy.limit
	if limit <= 0 || wakeBudgetUsed(a.mirror) < limit {
		key, label, limit = "wake.per_sender_per_hour", "sender", a.policy.senderLimit
		if limit <= 0 || wakeSenderUsed(a.mirror, sender) < limit {
			return false
		}
	}
	a.class = "wake.budget"
	a.fix = wakeBudgetSettingFix(a.key.AgentID, key, limit)
	a.held = fmt.Sprintf("held wake: %s rolling-hour budget %d is spent; %s", label, limit, a.fix)
	return true
}
