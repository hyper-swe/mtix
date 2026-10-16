// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// AttachProtocol is the dispatch protocol supported by this attach release (ADR-007 §6.4).
const AttachProtocol = 1

// WakeKey identifies one registration epoch's delivery of one event (ADR-007 §6.3).
type WakeKey struct {
	AgentID  string
	HostID   string
	UID      string
	EventSeq int64
	Epoch    int64
}

// WakeAttempt carries the durable fence issued to the winning claimant.
type WakeAttempt struct {
	Key       WakeKey
	Token     string
	ClaimedAt time.Time
}

// WakeOutcome is claimed until a send completes; both send outcomes are terminal.
type WakeOutcome string

const (
	WakeClaimed   WakeOutcome = "claimed"
	WakeDelivered WakeOutcome = "delivered"
	WakeError     WakeOutcome = "error"
)

// WakeLedgerStats counts retained deduplication and rejected-completion records.
type WakeLedgerStats struct {
	Rows     int64
	Claimed  int64
	Rejected int64
}
