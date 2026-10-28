// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

// WakeRecipient is the host-wide accounting unit, independent of board or epoch.
type WakeRecipient struct {
	AgentID string `json:"agent_id"`
	HostID  string `json:"host_id"`
	UID     string `json:"uid"`
}

// Recipient removes the event and registration epoch from a delivery key.
func (k WakeKey) Recipient() WakeRecipient {
	return WakeRecipient{AgentID: k.AgentID, HostID: k.HostID, UID: k.UID}
}

// WakeBudgetSnapshot is a transaction-consistent local accounting observation.
type WakeBudgetSnapshot struct {
	Source  string
	BoardID string
	Total   int64
	AckMark int64
}

// WakeBudgetProposal includes the fence before the admission decision is made.
type WakeBudgetProposal struct {
	Snapshot       WakeBudgetSnapshot
	BoardCandidate string
	Attempt        WakeAttempt
	Claimable      bool
	// InFlight is true when another claimant holds an unexpired, unfinished claim.
	InFlight bool
}

// WakeBudgetDecision supplies the protected counter floor and send admission.
type WakeBudgetDecision struct {
	BoardID string
	Floor   int64
	Allow   bool
}

// WakeBudgetResult returns only after the local transaction commits.
type WakeBudgetResult struct {
	Snapshot WakeBudgetSnapshot
	Attempt  WakeAttempt
	Won      bool
	Held     bool
	// InFlight reports a lost claim whose owner has not recorded an outcome yet.
	InFlight bool
}

// WakeBudgetCounter is one board's durable recipient counter for diagnostics.
type WakeBudgetCounter struct {
	Recipient WakeRecipient
	Source    string
	BoardID   string
	Total     int64
}
