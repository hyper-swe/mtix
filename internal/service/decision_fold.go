// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"

	"github.com/hyper-swe/mtix/internal/model"
)

// DecisionFoldEvent (ADR-008 §3.4, MTIX-219 decisions feature) contains the immutable identity, addressed comment fields
// and both results from model.DecodeDecision. Opaque redactions retain routing.
type DecisionFoldEvent struct {
	ID, Author, To string
	model.DecodedDecision
	DecodeErr error
	Redacted  bool
}

// NewDecisionFoldEvent projects a journal comment onto the fold's input.
// Spec: ADR-008 §3.4 (MTIX-219 decisions feature; REQUIREMENTS.md has no FR number for it yet).
func NewDecisionFoldEvent(event model.SyncEvent, to string, d model.DecodedDecision, decodeErr error) DecisionFoldEvent {
	return DecisionFoldEvent{ID: event.EventID, Author: event.AuthorID, To: to, DecodedDecision: d, DecodeErr: decodeErr}
}

// DecisionState is the closed set of card states defined in ADR-008 §3.4.
type DecisionState string

// States in fold priority order.
const (
	DecisionInvalid    DecisionState = "invalid"
	DecisionApplied    DecisionState = "applied"
	DecisionWithdrawn  DecisionState = "withdrawn"
	DecisionSuperseded DecisionState = "superseded"
	DecisionConflicted DecisionState = "conflicted"
	DecisionAnswered   DecisionState = "answered"
	DecisionOpen       DecisionState = "open"
)

// DecisionFoldCard (ADR-008 §3.4, MTIX-219 decisions feature) holds the derived state and fold flags of one canonical
// request. IDs refer to canonical events. Viewer flags are computed elsewhere.
type DecisionFoldCard struct {
	State                                                    DecisionState
	InvalidReason, PinnedAnswerID, EffectiveAnswer           string
	AgreeingAnswers, ConflictingAnswers, RefusedAnswers      []string
	ReopenedAfterRefusal, TicketDeleted, TicketMatchedByPath bool
	KeyConflict, DuplicateKeyConflict, OutcomeConflict       bool
	Redacted, UnsupportedEvents                              bool
}

type decisionGroup struct{ author, key string }

type decisionSet struct {
	members    []*DecisionFoldEvent
	aliases    map[string]string
	collisions map[decisionGroup]bool
}

// canonicalDecisions partitions by author/key, then by the received payload
// hash. Missing hashes or keys supply no evidence of equality.
func canonicalDecisions(events []DecisionFoldEvent) decisionSet {
	set := decisionSet{aliases: make(map[string]string), collisions: make(map[decisionGroup]bool)}
	groups := make(map[decisionGroup]map[string]*DecisionFoldEvent)
	for i := range events {
		e := &events[i]
		if e.Payload.RequestKey == "" || e.PayloadSHA256 == "" || e.Redacted {
			set.members = append(set.members, e)
			continue
		}
		group := decisionGroup{e.Author, e.Payload.RequestKey}
		hashes := groups[group]
		if hashes == nil {
			hashes = make(map[string]*DecisionFoldEvent)
			groups[group] = hashes
		}
		if prior := hashes[e.PayloadSHA256]; prior == nil || e.ID < prior.ID {
			hashes[e.PayloadSHA256] = e
		}
	}
	for group, hashes := range groups {
		set.collisions[group] = len(hashes) > 1
		for _, e := range hashes {
			set.members = append(set.members, e)
		}
	}
	for i := range events {
		e := &events[i]
		if !e.Redacted {
			if member := groups[decisionGroup{e.Author, e.Payload.RequestKey}][e.PayloadSHA256]; member != nil {
				set.aliases[e.ID] = member.ID
			}
		}
	}
	return set
}

func (s decisionSet) resolve(id string) string {
	if canonical, ok := s.aliases[id]; ok {
		return canonical
	}
	return id
}

func (e *DecisionFoldEvent) invalidReason() string {
	switch {
	case e.Redacted:
		return "redacted"
	case errors.Is(e.DecodeErr, model.ErrDecisionUnsupported):
		return "unsupported"
	case e.DecodeErr != nil:
		return "malformed"
	default:
		return ""
	}
}

// FoldDecisions computes ADR-008 §3.4 (MTIX-219 decisions feature; no FR number
// is assigned in REQUIREMENTS.md yet) over a set of journal decisions and
// deletes. Re-evaluate with the enlarged set when an orphan's request arrives.
func FoldDecisions(events []DecisionFoldEvent, deletes []DecisionDelete) map[string]DecisionFoldCard {
	set := canonicalDecisions(events)
	requests := make(map[string]*DecisionFoldEvent)
	replies := make(map[string][]*DecisionFoldEvent)
	for _, e := range set.members {
		if e.Payload.Kind == "request" {
			requests[e.ID] = e
			continue
		}
		target := set.resolve(e.Payload.Target)
		replies[target] = append(replies[target], e)
	}
	cards := make(map[string]DecisionFoldCard, len(requests))
	for id, r := range requests {
		cards[id] = set.foldRequest(r, replies[id], deletes)
	}
	applySupersession(requests, cards, set)
	return cards
}

func (s decisionSet) foldRequest(r *DecisionFoldEvent, replies []*DecisionFoldEvent, deletes []DecisionDelete) DecisionFoldCard {
	c := DecisionFoldCard{InvalidReason: r.invalidReason(), Redacted: r.Redacted, DuplicateKeyConflict: s.collisions[decisionGroup{r.Author, r.Payload.RequestKey}]}
	if c.InvalidReason == "" {
		if r.Payload.DecisionRequest == nil {
			c.InvalidReason = "malformed"
		} else {
			c.TicketDeleted, c.TicketMatchedByPath = DecisionTicketDeleted(r.Payload.Ticket, deletes)
			if c.TicketDeleted {
				c.InvalidReason = "ticket_deleted"
			}
		}
	}
	facts := newDecisionAnswers()
	for _, e := range replies {
		c.DuplicateKeyConflict = c.DuplicateKeyConflict || s.collisions[decisionGroup{e.Author, e.Payload.RequestKey}]
		switch e.invalidReason() {
		case "redacted":
			c.Redacted = true
		case "unsupported":
			c.UnsupportedEvents = true
		case "":
			if c.InvalidReason == "" {
				facts.collect(r, e, s)
			}
		}
	}
	if c.InvalidReason != "" {
		c.State = DecisionInvalid
		return c
	}
	facts.apply(&c)
	return c
}
