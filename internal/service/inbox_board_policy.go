// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// BoardInboxPolicyDeps wires the board's wake permissions into the inbox policy.
type BoardInboxPolicyDeps struct {
	Perms  *WakePermissions
	Store  PermissionStore
	Board  PermissionBoard
	State  *oplocal.State
	HostID string
	UID    string
	Clock  func() time.Time
}

// NewBoardInboxPolicy builds the wake policy for one board (ADR-007 A12): on a
// hub- or relay-connected board an id is eligible only when the operator
// confirmed it (D10; no principal filter in the beta, D17); on a board never
// connected every id is eligible. Registered ids are charged to the budget;
// an unregistered worker loop on a never-connected board is never capped.
func NewBoardInboxPolicy(d BoardInboxPolicyDeps) InboxPolicy {
	return InboxPolicy{
		Eligibility: func(ctx context.Context, agent string) (InboxEligibility, error) {
			status, err := NewHubMarkService(d.Store).Inspect(ctx, d.Board.Evidence)
			if err != nil {
				return InboxEligibility{Reason: "hub mark unreadable"}, fmt.Errorf("inspect hub mark: %w", err)
			}
			if status.Connected {
				ok, reason, wakeErr := d.Perms.Wakeable(ctx, d.Board, agent)
				if wakeErr != nil {
					return InboxEligibility{Reason: reason}, fmt.Errorf("check wakeable: %w", wakeErr)
				}
				return InboxEligibility{Eligible: ok, Reason: reason, Budgeted: ok}, nil
			}
			registered, regErr := d.Perms.registered(ctx, agent)
			if regErr != nil {
				return InboxEligibility{Eligible: true}, nil // never-connected board: fail open
			}
			return InboxEligibility{Eligible: true, Reason: "board never connected", Budgeted: registered}, nil
		},
		Budget: WakeInboxBudget{State: d.State, HostID: d.HostID, UID: d.UID, Clock: d.Clock},
	}
}

// ConnectedInboxPolicy is the fail-closed policy for a hub-connected board whose
// permission state cannot be read: every event is held with the reason.
func ConnectedInboxPolicy(reason string) InboxPolicy {
	return InboxPolicy{Eligibility: func(context.Context, string) (InboxEligibility, error) {
		return InboxEligibility{Reason: reason}, nil
	}}
}
