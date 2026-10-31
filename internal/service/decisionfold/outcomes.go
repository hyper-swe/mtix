// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import (
	"slices"

	"github.com/hyper-swe/mtix/internal/model"
)

// byAsker returns the valid events by the request author among related, of
// the given kind.
func (f *folder) byAsker(req Event, related []string, kind model.DecisionKind) []Event {
	var out []Event
	for _, rid := range related {
		e := f.idx.events[rid]
		if e.AuthorID == req.AuthorID && e.Class == model.DecisionValid && e.Decision != nil && e.Decision.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// appliedOutcome returns the pinned answer and the applied outcomes by the
// asker that name an answer in V. The pinned answer is the minimum event id
// among the named answers.
func (f *folder) appliedOutcome(req Event, related []string, sets answerSets) (pinned string, outcomes []string) {
	for _, o := range f.byAsker(req, related, model.DecisionKindOutcome) {
		if o.Decision.Outcome.Result != model.DecisionResultApplied {
			continue
		}
		named := f.idx.resolve(o.Decision.Outcome.Answer)
		if !slices.Contains(sets.valid, named) {
			continue
		}
		outcomes = append(outcomes, o.EventID)
		if pinned == "" || named < pinned {
			pinned = named
		}
	}
	return pinned, outcomes
}

// withdraws returns the valid withdraws by the asker.
func (f *folder) withdraws(req Event, related []string) []string {
	var out []string
	for _, w := range f.byAsker(req, related, model.DecisionKindWithdraw) {
		out = append(out, w.EventID)
	}
	return out
}

// outcomeConflict reports the conditions that add outcome_conflict to an
// applied card: applied outcomes naming answers of different values, a
// refusal by the asker that names the pinned answer, or a withdraw.
func (f *folder) outcomeConflict(sets answerSets, pinned string, applied []string, withdrawn bool) bool {
	if withdrawn || sets.refused[pinned] {
		return true
	}
	want := f.answerValue(pinned)
	for _, oid := range applied {
		named := f.idx.resolve(f.idx.events[oid].Decision.Outcome.Answer)
		if f.answerValue(named) != want {
			return true
		}
	}
	return false
}

// refusedIn returns X restricted to V, in event id order.
func (f *folder) refusedIn(sets answerSets) []string {
	var out []string
	for _, v := range sets.valid {
		if sets.refused[v] {
			out = append(out, v)
		}
	}
	return out
}

// sameAs returns the answers whose value equals the value of answer id.
func (f *folder) sameAs(id string, ids []string) []string {
	want := f.answerValue(id)
	var out []string
	for _, a := range ids {
		if f.answerValue(a) == want {
			out = append(out, a)
		}
	}
	return out
}

// subtract returns the members of all that are not in remove.
func subtract(all, remove []string) []string {
	var out []string
	for _, a := range all {
		if !slices.Contains(remove, a) {
			out = append(out, a)
		}
	}
	return out
}
