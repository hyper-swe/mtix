// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func wakeBackoffDelay(unacked, threshold int64) time.Duration {
	if unacked < threshold {
		return 0
	}
	exponent := unacked - threshold
	if exponent >= 5 {
		return 30 * time.Minute
	}
	return time.Minute * time.Duration(int64(1)<<exponent)
}

func setWakeBackoffDeadline(policy wakeBudgetPolicy, m *oplocal.WakeBudgetMirror) {
	delay := wakeBackoffDelay(m.Unacked, policy.backoff)
	if delay == 0 {
		m.NextWakeAt = time.Time{}
		return
	}
	m.NextWakeAt = m.LastWakeAt.Add(delay)
}

func holdWakeBackoff(agent string, policy wakeBudgetPolicy, now time.Time,
	m *oplocal.WakeBudgetMirror, a *wakeAdmission,
) bool {
	setWakeBackoffDeadline(policy, m)
	if m.NextWakeAt.IsZero() || !now.Before(m.NextWakeAt) {
		return false
	}
	threshold := m.Unacked
	if threshold < math.MaxInt64 {
		threshold++
	}
	a.class = oplocal.AlertClassWakeBackoff
	a.fix = "mtix config set wake.backoff_after_unacked " + strconv.FormatInt(threshold, 10) + " --agent " + wakeBudgetAgentArgument(agent)
	a.held = fmt.Sprintf("held wake: %d consecutive unacknowledged wakes; backoff until %s [chain: event %d, %d unacked wakes]; %s",
		m.Unacked, m.NextWakeAt.Format(time.RFC3339Nano), a.key.EventSeq, m.Unacked, a.fix)
	return true
}
