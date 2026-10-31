// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

type wakeBudgetPolicy struct {
	limit       int64
	senderLimit int64
	backoff     int64
}

func (b *wakeBudgets) policy(agent string) (wakeBudgetPolicy, error) {
	values, reports, err := b.settings.WakeSettings(agent)
	if err != nil {
		return wakeBudgetPolicy{}, fmt.Errorf("read wake budget policy: %w", err)
	}
	if len(reports) != 0 {
		return wakeBudgetPolicy{}, fmt.Errorf("wake budget policy belongs to another host: %w", model.ErrOperatorStateUnreadable)
	}
	threshold, err := strconv.ParseInt(values["wake.backoff_after_unacked"].Value, 10, 64)
	if err != nil || threshold <= 0 {
		return wakeBudgetPolicy{}, fmt.Errorf("wake backoff policy must be positive: %w", model.ErrInvalidInput)
	}
	policy := wakeBudgetPolicy{backoff: threshold}
	policy.senderLimit, err = wakeSenderLimit(values["wake.per_sender_per_hour"].Value)
	if err != nil {
		return policy, err
	}
	value := values["wake.wakes_per_hour"].Value
	if value == "" {
		return policy, nil
	}
	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil || limit <= 0 {
		return wakeBudgetPolicy{}, fmt.Errorf("wake frequency policy is invalid: %w", model.ErrInvalidInput)
	}
	policy.limit = limit
	return policy, nil
}

func wakeBudgetSettingFix(agent, key string, limit int64) string {
	value := "unset"
	if limit < math.MaxInt64 {
		value = strconv.FormatInt(limit+1, 10)
	}
	return "mtix config set " + key + " " + value + " --agent " + wakeBudgetAgentArgument(agent)
}

func wakeBudgetAgentArgument(agent string) string {
	for _, r := range agent {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return "'" + strings.ReplaceAll(agent, "'", "'\\''") + "'"
	}
	return agent
}

func (b *wakeBudgets) reportAdmission(ctx context.Context, agent string, a wakeAdmission) error {
	var failures error
	if a.rewound {
		_, err := b.alerts.Raise(ctx, AlertRequest{Agent: agent, Class: oplocal.AlertClassBudgetRewind,
			Detail: "store wake counter went backwards; protected allowance was retained",
			Fix:    "Preserve the operator-local budget mirror; inspect the restored board with mtix doctor --json."})
		if err != nil {
			failures = fmt.Errorf("record wake counter rewind: %w", err)
		}
	}
	if a.held != "" {
		b.held(a.held)
		_, err := b.alerts.Raise(ctx, AlertRequest{Agent: agent, Class: a.class, Detail: a.held, Fix: a.fix})
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("record wake budget hold: %w", err))
		}
	}
	return failures
}
