// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// IdentityEnv carries explicit launcher identity and the resumed session handle.
// Proposals are caller-discovered candidates; they never authorize a choice.
type IdentityEnv struct {
	AgentID, SessionHandle string
	Proposals              []string
}

// AgentIDResolution distinguishes a proven identity from suggestions.
type AgentIDResolution struct {
	ID        string
	Silent    bool
	Proposals []string
	Reason    string
}

// ResolveAgentID implements ADR-007 section 3.1 without guessing an identity.
// Explicit environment and a resumed handle precede all learned-lane records.
func (s *RegistrationService) ResolveAgentID(ctx context.Context, loc oplocal.LaneLocation, env IdentityEnv) (AgentIDResolution, error) {
	if env.AgentID != "" {
		if err := validateAgentID(env.AgentID); err != nil {
			return AgentIDResolution{}, err
		}
		return AgentIDResolution{ID: env.AgentID, Silent: true, Reason: "environment"}, nil
	}
	rows, err := s.Store.ListRegistrations(ctx, s.Host.HostID, s.Host.UID)
	if err != nil {
		return AgentIDResolution{}, fmt.Errorf("resolve registration identity: %w", err)
	}
	resumed := resumedLaneIDs(rows, loc.Harness, env.SessionHandle)
	if len(resumed) == 1 {
		return AgentIDResolution{ID: resumed[0], Silent: true, Reason: "resumed-session"}, nil
	}
	if len(resumed) > 1 {
		return AgentIDResolution{Proposals: resumed, Reason: "ambiguous-session"}, nil
	}
	if s.Routing == nil || s.Routing.State == nil {
		return AgentIDResolution{}, fmt.Errorf("learned identity routing is unavailable: %w", model.ErrInvalidInput)
	}
	if loc.BoardKey != s.Routing.Board.Key || loc.Root != s.Routing.Board.Path {
		return AgentIDResolution{}, fmt.Errorf("identity location belongs to another board: %w", model.ErrInvalidInput)
	}
	learned, err := s.Routing.State.LaneIDs(ctx, loc)
	if err != nil {
		return AgentIDResolution{}, err
	}
	if len(learned) == 1 && !s.identityIsLive(ctx, rows, learned[0]) {
		return AgentIDResolution{ID: learned[0], Silent: true, Reason: "learned-lane"}, nil
	}
	return s.identityProposals(ctx, rows, learned, env.Proposals), nil
}

func resumedLaneIDs(rows []sqlite.Registration, harness, handle string) []string {
	var ids []string
	if handle == "" {
		return ids
	}
	for _, row := range rows {
		if row.Harness == harness && row.SessionHandle == handle {
			ids = append(ids, row.AgentID)
		}
	}
	sort.Strings(ids)
	return ids
}
func (s *RegistrationService) identityIsLive(ctx context.Context, rows []sqlite.Registration, id string) bool {
	for _, row := range rows {
		if row.AgentID == id {
			if s.Liveness == nil || s.Liveness.Probe(ctx, row) != LiveDead {
				return true
			}
		}
	}
	return false
}
func (s *RegistrationService) identityProposals(ctx context.Context, rows []sqlite.Registration, learned, candidates []string) AgentIDResolution {
	result := AgentIDResolution{Reason: "register-required"}
	if len(learned) > 1 {
		result.Reason = "ambiguous-lane"
	} else if len(learned) == 1 {
		result.Reason = "lane-in-use"
	}
	seen := map[string]bool{}
	for _, set := range [][]string{learned, candidates} {
		for _, id := range set {
			if id != "" && !seen[id] && validateAgentID(id) == nil && !s.identityIsLive(ctx, rows, id) {
				result.Proposals = append(result.Proposals, id)
				seen[id] = true
			}
		}
	}
	sort.Strings(result.Proposals)
	return result
}
