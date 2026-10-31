// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import (
	"cmp"
	"slices"

	"github.com/hyper-swe/mtix/internal/model"
)

// folder holds what is derived once from an event set and a delete set.
type folder struct {
	idx     *index
	deletes []Delete
	// reason is the rule 1 verdict for every request.
	reason map[string]InvalidReason
	graph  *supersedeGraph
}

// Fold returns the card of the request requestID. events is the set of
// decision events that can bear on it: the request, every answer, outcome
// and withdraw that names it, and the other requests that may supersede it
// or share its key. deletes is the set of delete events. The result depends
// only on these two sets, never on their order or on anything else.
func Fold(requestID string, events []Event, deletes []Delete) Card {
	f := &folder{idx: buildIndex(events), deletes: deletes, reason: map[string]InvalidReason{}}
	id := f.idx.resolve(requestID)
	req, ok := f.idx.events[id]
	if !ok || !requestLike(req) {
		return Card{RequestID: requestID}
	}
	for _, rid := range f.requestIDs() {
		f.reason[rid], _ = f.invalidReason(f.idx.events[rid])
	}
	f.graph = f.buildGraph()
	card := f.card(id)
	if card.State == StateOpen && f.hasOpenPeer(id) {
		card.Flags = withFlag(card.Flags, FlagKeyConflict)
	}
	return card
}

// requestIDs returns the canonical ids of the request events in order.
func (f *folder) requestIDs() []string {
	var out []string
	for _, id := range f.idx.sortedIDs() {
		if requestLike(f.idx.events[id]) {
			out = append(out, id)
		}
	}
	return out
}

// requestLike reports whether an event is a request, or an object that did
// not parse far enough to say it is anything else. The second kind folds to
// an invalid card.
func requestLike(e Event) bool {
	switch e.Kind {
	case model.DecisionKindRequest:
		return true
	case model.DecisionKindAnswer, model.DecisionKindOutcome, model.DecisionKindWithdraw:
		return false
	}
	return e.Kind == "" && e.Class == model.DecisionMalformed
}

// invalidReason applies the first half of rule 1 and the ticket predicate.
func (f *folder) invalidReason(e Event) (InvalidReason, ticketVerdict) {
	var v ticketVerdict
	if e.Class == model.DecisionValid && e.Decision != nil && e.Decision.Request != nil {
		v.deleted, v.byUID = ticketDeleted(e.Decision.Request.Ticket, f.deletes)
	}
	switch {
	case e.Class == model.DecisionUnsupported:
		return ReasonUnsupported, v
	case e.Class != model.DecisionValid || e.Decision == nil || e.Decision.Request == nil:
		return ReasonMalformed, v
	case e.Redacted:
		return ReasonRedacted, v
	case v.deleted:
		return ReasonTicketDeleted, v
	}
	return ReasonNone, v
}

type ticketVerdict struct{ deleted, byUID bool }

// related returns the canonical ids, in order, of the events that name the
// request. The request itself is not included.
func (f *folder) related(id string) []string {
	var out []string
	for _, eid := range f.idx.sortedIDs() {
		e := f.idx.events[eid]
		if eid != id && e.Target != "" && f.idx.resolve(e.Target) == id {
			out = append(out, eid)
		}
	}
	return out
}

// card folds one request without looking at its peers' keys.
func (f *folder) card(id string) Card {
	req := f.idx.events[id]
	related := f.related(id)
	card := Card{Present: true, RequestID: id, State: StateOpen}
	flags := f.commonFlags(req, id, related)
	if f.graph.inCycle(id) {
		flags[FlagKeyConflict] = true
	}
	if why := f.reason[id]; why != ReasonNone {
		card.State, card.InvalidReason = StateInvalid, why
		card.Flags, card.Timeline = orderedFlags(flags), f.timeline(id, related, nil)
		return card
	}
	sets := f.collectAnswers(id, related)
	f.decide(&card, req, related, sets, flags)
	card.Flags = orderedFlags(flags)
	card.Timeline = f.timeline(id, related, &sets)
	return card
}

// commonFlags sets the flags that do not depend on the state.
func (f *folder) commonFlags(req Event, id string, related []string) map[Flag]bool {
	flags := map[Flag]bool{}
	if req.Redacted {
		flags[FlagRedacted] = true
	}
	if f.idx.conflicted[id] {
		flags[FlagDuplicateKeyConflict] = true
	}
	for _, rid := range related {
		e := f.idx.events[rid]
		flags[FlagUnsupportedEvents] = flags[FlagUnsupportedEvents] || e.Class == model.DecisionUnsupported
		flags[FlagDuplicateKeyConflict] = flags[FlagDuplicateKeyConflict] || f.idx.conflicted[rid]
	}
	if _, v := f.invalidReason(req); v.deleted {
		flags[FlagTicketDeleted] = true
		flags[FlagTicketMatchedByPath] = !v.byUID
	}
	return flags
}

// decide applies rules 2 to 7 to a request that rule 1 left alone.
func (f *folder) decide(card *Card, req Event, related []string, sets answerSets, flags map[Flag]bool) {
	id := card.RequestID
	card.RefusedAnswers = f.refusedIn(sets)
	pinned, applied := f.appliedOutcome(req, related, sets)
	withdraws := f.withdraws(req, related)
	card.WithdrawIDs = withdraws
	agree, differ := f.partition(sets.active)
	switch {
	case len(applied) > 0:
		card.State, card.PinnedAnswerID, card.EffectiveAnswerID = StateApplied, pinned, pinned
		card.OutcomeIDs = applied
		card.AgreeingAnswers = f.sameAs(pinned, sets.active)
		card.ConflictingAnswers = subtract(sets.active, card.AgreeingAnswers)
		flags[FlagOutcomeConflict] = f.outcomeConflict(sets, pinned, applied, len(withdraws) > 0)
	case len(withdraws) > 0:
		card.State = StateWithdrawn
	case len(f.graph.supersededBy(id)) > 0:
		card.State, card.SupersededBy = StateSuperseded, f.graph.supersededBy(id)
	case len(differ) > 0:
		card.State, card.ConflictingAnswers = StateConflicted, slices.Clone(sets.active)
	case len(agree) > 0:
		card.State, card.AgreeingAnswers, card.EffectiveAnswerID = StateAnswered, agree, agree[0]
	default:
		flags[FlagReopenedAfterRefusal] = len(sets.refused) > 0
	}
	if !flags[FlagOutcomeConflict] {
		delete(flags, FlagOutcomeConflict)
	}
}

// orderedFlags lists the set flags in the fixed flag order.
func orderedFlags(set map[Flag]bool) []Flag {
	var out []Flag
	for _, fl := range flagOrder {
		if set[fl] {
			out = append(out, fl)
		}
	}
	return out
}

func withFlag(flags []Flag, f Flag) []Flag {
	set := map[Flag]bool{f: true}
	for _, x := range flags {
		set[x] = true
	}
	return orderedFlags(set)
}

// hasOpenPeer reports whether another request by the same asker with the
// same key is also open.
func (f *folder) hasOpenPeer(id string) bool {
	req := f.idx.events[id]
	if req.Decision == nil || req.Decision.Request == nil || req.Decision.Request.Key == "" {
		return false
	}
	for _, pid := range f.requestIDs() {
		peer := f.idx.events[pid]
		if pid == id || f.reason[pid] != ReasonNone || peer.AuthorID != req.AuthorID ||
			peer.Decision.Request.Key != req.Decision.Request.Key {
			continue
		}
		if f.card(pid).State == StateOpen {
			return true
		}
	}
	return false
}

// timeline lists the request and the events that name it, ordered by
// wall-clock time and event id. The order is for display only.
func (f *folder) timeline(id string, related []string, sets *answerSets) []TimelineEntry {
	req := f.idx.events[id]
	out := []TimelineEntry{entry(req, true)}
	for _, rid := range related {
		e := f.idx.events[rid]
		out = append(out, entry(e, f.counted(req, e, sets)))
	}
	slices.SortFunc(out, func(a, b TimelineEntry) int {
		return cmp.Or(cmp.Compare(a.WallClockTS, b.WallClockTS), cmp.Compare(a.EventID, b.EventID))
	})
	return out
}

func entry(e Event, counted bool) TimelineEntry {
	return TimelineEntry{EventID: e.EventID, Kind: e.Kind, AuthorID: e.AuthorID, WallClockTS: e.WallClockTS, Counted: counted}
}

// counted reports whether an event takes part in the state.
func (f *folder) counted(req, e Event, sets *answerSets) bool {
	if sets == nil || e.Class != model.DecisionValid || e.Decision == nil {
		return false
	}
	if e.Decision.Kind == model.DecisionKindAnswer {
		return slices.Contains(sets.valid, e.EventID)
	}
	return e.AuthorID == req.AuthorID
}
