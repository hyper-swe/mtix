// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeExecContext is process-carried context for a delivered wake (FR-20.3).
// Attempt is an audit label, never an authorization or ledger claim.
type WakeExecContext struct {
	Attempt string
	Hops    []int64
}

// WakeContextForAttempt carries a fenced attempt into an exec child.
func WakeContextForAttempt(attempt model.WakeAttempt) *WakeExecContext {
	return &WakeExecContext{Attempt: attempt.Token, Hops: []int64{attempt.Hop}}
}

func wakeExecEnvironment(wake *WakeExecContext) ([]string, error) {
	if wake == nil {
		return nil, nil
	}
	if wake.Attempt == "" || len(wake.Attempt) > 128 || strings.ContainsAny(wake.Attempt, "\x00\r\n") || len(wake.Hops) == 0 {
		return nil, fmt.Errorf("wake context requires an audit attempt and covered events: %w", model.ErrInvalidInput)
	}
	var maximum int64
	for _, hop := range wake.Hops {
		if hop < 0 {
			return nil, fmt.Errorf("wake hop must be nonnegative: %w", model.ErrInvalidInput)
		}
		if hop > maximum {
			maximum = hop
		}
	}
	return []string{"MTIX_WAKE_HOP=" + strconv.FormatInt(maximum, 10), "MTIX_WAKE_ATTEMPT=" + wake.Attempt}, nil
}
