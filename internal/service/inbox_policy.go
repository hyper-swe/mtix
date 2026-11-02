// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// HeldReasonBudget is the reason recorded when a configured budget refuses
// events that would otherwise be returned (ADR-007 D4, A12).
const HeldReasonBudget = "budget"

// InboxEligibility is the wake policy's verdict for one agent id (ADR-007 A12).
// Eligible false holds every event for the id (D10); Budgeted says the id's
// returns are charged to the configured budget.
type InboxEligibility struct {
	Eligible bool
	Reason   string
	Budgeted bool
}

// InboxBudget charges a configured budget for events returned to an agent id.
type InboxBudget interface {
	// Admit atomically charges up to want events and returns how many were
	// granted. With no budget configured it grants all of them (D4).
	Admit(ctx context.Context, agent string, want int64) (int64, error)
}

// InboxMarkStore keeps the returned-seq mark that makes a budget charge only
// events not returned to the id before (ADR-007 A12).
type InboxMarkStore interface {
	InboxReturnedMark(ctx context.Context, agent string) (int64, error)
	AdvanceInboxReturnedMark(ctx context.Context, agent string, seq int64) error
}

// InboxPolicy is the wake policy every agent-facing inbox read applies
// (ADR-007 A12). The zero value applies nothing: every event is returned.
type InboxPolicy struct {
	Eligibility func(ctx context.Context, agent string) (InboxEligibility, error)
	Budget      InboxBudget
}

// InboxHeld counts events withheld by the policy; bodies are never included.
type InboxHeld struct {
	Count   int      `json:"count"`
	Reasons []string `json:"reasons"`
}

// InboxResult is what a policy-applying read returns (ADR-007 A12). HeldEvents
// is filled only for an explicit IncludeHeld read.
type InboxResult struct {
	Events     []InboxEvent `json:"events"`
	Held       InboxHeld    `json:"held"`
	HeldEvents []InboxEvent `json:"held_events,omitempty"`
}

// InboxReadOptions tunes one policy-applying read.
type InboxReadOptions struct {
	// IncludeHeld also returns held events' bodies in HeldEvents. The caller
	// is responsible for refusing it under a harness session marker.
	IncludeHeld bool
}

// WithInboxPolicy sets the wake policy applied by InboxRead and InboxReadWait.
func (s *InboxService) WithInboxPolicy(p InboxPolicy) *InboxService {
	s.policy = p
	return s
}

// InboxRead lists the unacknowledged events the wake policy allows for agent
// and the count of events it holds (ADR-007 A12). New returns are charged once
// to the budget through the returned-seq mark.
func (s *InboxService) InboxRead(ctx context.Context, agent string, opt InboxReadOptions) (InboxResult, error) {
	if s.reader == nil {
		return InboxResult{}, wrapAPIOperation("read inbox", model.ErrInvalidInput)
	}
	events, err := s.reader.InboxList(ctx, agent)
	if err != nil {
		return InboxResult{}, wrapAPIOperation("read inbox", err)
	}
	res, err := s.applyPolicy(ctx, agent, inboxEvents(events), opt)
	if err != nil {
		return InboxResult{}, wrapAPIOperation("apply inbox policy", err)
	}
	notifyReevaluator(ctx, s.reeval, agent, "inbox list")
	return res, nil
}

func (s *InboxService) applyPolicy(ctx context.Context, agent string, events []InboxEvent, opt InboxReadOptions) (InboxResult, error) {
	res := InboxResult{Events: []InboxEvent{}, Held: InboxHeld{Reasons: []string{}}}
	if len(events) == 0 {
		return res, nil
	}
	if s.policy.Eligibility == nil {
		res.Events = events
		return res, nil
	}
	el, err := s.policy.Eligibility(ctx, agent)
	if err != nil {
		// Fail closed: an unreadable policy holds everything and says why.
		el = InboxEligibility{Reason: "policy unavailable: " + err.Error()}
	}
	if !el.Eligible {
		return s.holdAll(res, events, el.Reason, opt), nil
	}
	if !el.Budgeted || s.policy.Budget == nil {
		res.Events = events
		return res, nil
	}
	return s.chargeNew(ctx, agent, events, res, opt)
}

func (s *InboxService) holdAll(res InboxResult, events []InboxEvent, reason string, opt InboxReadOptions) InboxResult {
	res.Held = InboxHeld{Count: len(events), Reasons: []string{reason}}
	if opt.IncludeHeld {
		res.HeldEvents = events
	}
	return res
}

// chargeNew returns events already returned before at no charge, charges the
// budget once for the rest and holds what the budget refuses (A12).
func (s *InboxService) chargeNew(ctx context.Context, agent string, events []InboxEvent, res InboxResult, opt InboxReadOptions) (InboxResult, error) {
	marks, ok := s.reader.(InboxMarkStore)
	if !ok {
		res.Events = events
		return res, nil
	}
	mark, err := marks.InboxReturnedMark(ctx, agent)
	if err != nil {
		return res, fmt.Errorf("read returned mark: %w", err)
	}
	var fresh []InboxEvent
	for _, e := range events {
		if e.Seq <= mark {
			res.Events = append(res.Events, e)
			continue
		}
		fresh = append(fresh, e)
	}
	if len(fresh) == 0 {
		return res, nil
	}
	granted, err := s.policy.Budget.Admit(ctx, agent, int64(len(fresh)))
	if err != nil {
		return res, fmt.Errorf("charge inbox budget: %w", err)
	}
	granted = max(0, min(granted, int64(len(fresh))))
	if granted > 0 {
		res.Events = append(res.Events, fresh[:granted]...)
		if err := marks.AdvanceInboxReturnedMark(ctx, agent, fresh[granted-1].Seq); err != nil {
			return res, fmt.Errorf("advance returned mark: %w", err)
		}
	}
	if rest := fresh[granted:]; len(rest) > 0 {
		res.Held = InboxHeld{Count: len(rest), Reasons: []string{HeldReasonBudget}}
		if opt.IncludeHeld {
			res.HeldEvents = rest
		}
	}
	return res, nil
}
