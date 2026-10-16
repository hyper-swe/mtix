// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"github.com/hyper-swe/mtix/internal/hooks"
	"sort"
)

// PermissionCandidate contains three independent facts; selection always starts false.
type PermissionCandidate struct {
	AgentID                   string `json:"agent_id"`
	RegisteredOnHost          bool   `json:"registered_on_host"`
	AddressedByForeignSenders bool   `json:"addressed_by_foreign_senders"`
	NamedInInstructions       bool   `json:"named_in_instructions"`
	Selected                  bool   `json:"selected"`
}

// Candidates is reusable by future setup; it does not confirm or change anything.
func (s *WakePermissions) Candidates(ctx context.Context, board PermissionBoard) ([]PermissionCandidate, error) {
	registered, err := s.Store.ListRegistrations(ctx, s.HostID, s.UID)
	if err != nil {
		return nil, err
	}
	addressed, err := s.Store.PermissionAddressedAgents(ctx)
	if err != nil {
		return nil, err
	}
	facts := map[string]PermissionCandidate{}
	for _, row := range registered {
		facts[row.AgentID] = PermissionCandidate{AgentID: row.AgentID, RegisteredOnHost: true}
	}
	for _, id := range addressed {
		if identityErr := validateAgentID(id); identityErr != nil {
			return nil, identityErr
		}
		row := facts[id]
		row.AgentID, row.AddressedByForeignSenders = id, true
		facts[id] = row
	}
	ids := make([]string, 0, len(facts))
	for id := range facts {
		ids = append(ids, id)
	}
	named, err := hooks.InstructionAgents(board.Checkout, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range named {
		if identityErr := validateAgentID(id); identityErr != nil {
			return nil, identityErr
		}
		row := facts[id]
		row.AgentID, row.NamedInInstructions = id, true
		facts[id] = row
	}
	result := make([]PermissionCandidate, 0, len(facts))
	for _, row := range facts {
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AgentID < result[j].AgentID })
	return result, nil
}
