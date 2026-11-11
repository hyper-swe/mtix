// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
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

// AdmitFunc charges the budget for events from the given senders (one entry per
// event, in order) and returns how many were granted, always a prefix. Each
// credit carries its event's sender so wake.per_sender_per_hour counts it only
// against that sender (A12). It is only valid inside InboxBudget.Locked.
type AdmitFunc func(senders []string) (int64, error)

// InboxBudget charges a configured budget for events returned to an agent id.
// Locked runs fn inside the budget's critical section, the same one the wake
// dispatcher charges in, so a read and a wake for one event never both charge
// it and two concurrent reads never charge it twice (ADR-007 A12).
// With no budget configured fn runs without a lock and admit grants all.
type InboxBudget interface {
	Locked(ctx context.Context, agent string, fn func(admit AdmitFunc) error) error
}

// InboxChargeStore reports the events the wake dispatcher already charged for
// an agent id (rows in the wake ledger), so a read does not charge them again.
type InboxChargeStore interface {
	WakeChargedAfter(ctx context.Context, agent string, after int64) (map[int64]bool, error)
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
	notifyReevaluator(ctx, s.logger, s.reeval, agent, "inbox list")
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

// chargeNew runs the charge inside the budget's critical section (A12).
func (s *InboxService) chargeNew(ctx context.Context, agent string, events []InboxEvent, res InboxResult, opt InboxReadOptions) (InboxResult, error) {
	marks, ok := s.reader.(InboxMarkStore)
	if !ok {
		res.Events = events
		return res, nil
	}
	err := s.policy.Budget.Locked(ctx, agent, func(admit AdmitFunc) error {
		var chargeErr error
		res, chargeErr = s.chargeLocked(ctx, agent, events, res, opt, marks, admit)
		return chargeErr
	})
	if err != nil {
		return res, fmt.Errorf("charge inbox budget: %w", err)
	}
	return res, nil
}

// chargeLocked returns events already returned before, and events the wake
// dispatcher already charged, at no charge; it charges the budget once for the
// rest, holds what the budget refuses and advances the returned mark up to the
// first held event (A12).
func (s *InboxService) chargeLocked(ctx context.Context, agent string, events []InboxEvent, res InboxResult, opt InboxReadOptions, marks InboxMarkStore, admit AdmitFunc) (InboxResult, error) {
	mark, err := marks.InboxReturnedMark(ctx, agent)
	if err != nil {
		return res, fmt.Errorf("read returned mark: %w", err)
	}
	returned, fresh := splitReturned(events, mark)
	res.Events = append(res.Events, returned...)
	if len(fresh) == 0 {
		return res, nil
	}
	charged := map[int64]bool{}
	if cs, ok := s.reader.(InboxChargeStore); ok {
		if charged, err = cs.WakeChargedAfter(ctx, agent, mark); err != nil {
			return res, fmt.Errorf("read dispatcher charges: %w", err)
		}
	}
	granted, err := admit(neededSenders(fresh, charged))
	if err != nil {
		return res, fmt.Errorf("admit inbox events: %w", err)
	}
	give, held := allocate(fresh, charged, granted)
	res.Events = append(res.Events, give...)
	next := fresh[len(fresh)-1].Seq
	if len(held) > 0 {
		next = held[0].Seq - 1
		res.Held = InboxHeld{Count: len(held), Reasons: []string{HeldReasonBudget}}
		if opt.IncludeHeld {
			res.HeldEvents = held
		}
	}
	if next > mark {
		if err := marks.AdvanceInboxReturnedMark(ctx, agent, next); err != nil {
			return res, fmt.Errorf("advance returned mark: %w", err)
		}
	}
	return res, nil
}

// InboxCountPolicy is InboxCount restricted to what a read would return: an id
// the policy holds reports 0 pending and the count it held (A12). It reads only.
func (s *InboxService) InboxCountPolicy(ctx context.Context, agent string) (pending, total, held int64, err error) {
	pending, total, err = s.InboxCount(ctx, agent)
	if err != nil || pending == 0 || s.policy.Eligibility == nil {
		return pending, total, 0, err
	}
	el, eligErr := s.policy.Eligibility(ctx, agent)
	if eligErr == nil && el.Eligible {
		return pending, total, 0, nil
	}
	return 0, total, pending, nil
}

// splitReturned separates events already returned before (seq at or below the
// mark) from fresh ones.
func splitReturned(events []InboxEvent, mark int64) (returned, fresh []InboxEvent) {
	for _, e := range events {
		if e.Seq <= mark {
			returned = append(returned, e)
		} else {
			fresh = append(fresh, e)
		}
	}
	return returned, fresh
}

// neededSenders lists, in order, the normalized sender of each fresh event the
// dispatcher has not already charged; the read charges one credit per entry.
func neededSenders(fresh []InboxEvent, charged map[int64]bool) []string {
	var senders []string
	for _, e := range fresh {
		if !charged[e.Seq] {
			senders = append(senders, oplocal.NormalizeWakeBudgetSender(e.Author))
		}
	}
	return senders
}

// allocate gives every dispatcher-charged event, and as many others as the
// budget granted, in order; the rest are held.
func allocate(fresh []InboxEvent, charged map[int64]bool, granted int64) (give, held []InboxEvent) {
	granted = max(0, granted)
	for _, e := range fresh {
		switch {
		case charged[e.Seq]:
			give = append(give, e)
		case granted > 0:
			granted--
			give = append(give, e)
		default:
			held = append(held, e)
		}
	}
	return give, held
}
