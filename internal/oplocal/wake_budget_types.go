// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeBudgetCredit preserves the exact rolling-window time of charged attempts.
type WakeBudgetCredit struct {
	At    time.Time `json:"at"`
	Count int64     `json:"count"`
}

// WakeBudgetSource preserves both accepted and conservatively pending counters.
type WakeBudgetSource struct {
	Total     int64  `json:"total"`
	Committed int64  `json:"committed"`
	AckMark   int64  `json:"ack_mark"`
	Pending   string `json:"pending,omitempty"`
}

// WakeBudgetMirror is protected accounting for one recipient across all boards.
type WakeBudgetMirror struct {
	Version      int                         `json:"version"`
	Recipient    model.WakeRecipient         `json:"recipient"`
	Sources      map[string]WakeBudgetSource `json:"sources"`
	Credits      []WakeBudgetCredit          `json:"credits"`
	Unacked      int64                       `json:"unacked"`
	NextWakeAt   time.Time                   `json:"next_wake_at,omitempty"`
	ObservedAt   time.Time                   `json:"observed_at,omitempty"`
	LastWakeAt   time.Time                   `json:"last_wake_at,omitempty"`
	Rewinds      uint64                      `json:"rewinds"`
	LastRewindAt time.Time                   `json:"last_rewind_at,omitempty"`
}
