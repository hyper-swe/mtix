// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import (
	"slices"

	"github.com/hyper-swe/mtix/internal/model"
)

// cliAuthor is the author id that never counts as a decider.
const cliAuthor = "cli"

// answerSets holds the answer groups of one request.
type answerSets struct {
	// valid is V: the answers that may take part, in event id order.
	valid []string
	// refused is X: the answers a refused outcome by the asker names.
	refused map[string]bool
	// active is A: V minus X, in event id order.
	active []string
}

// answerValue returns the comparison value of an answer event.
func (f *folder) answerValue(id string) model.DecisionAnswerValue {
	return f.idx.events[id].Decision.Answer.Value()
}

// eligible reports whether an answer event counts for the request.
func eligible(req Event, ans Event) bool {
	if ans.Class != model.DecisionValid || ans.Decision == nil || ans.Decision.Answer == nil {
		return false
	}
	r := req.Decision.Request
	switch {
	case ans.AuthorID == req.AuthorID, ans.AuthorID == cliAuthor:
		return false
	case ans.AuthorID != req.To && !slices.Contains(r.Answerers, ans.AuthorID):
		return false
	}
	return model.ValidateDecisionAnswer(r, ans.Decision.Answer) == nil
}

// collectAnswers computes V, X and A for the request id.
func (f *folder) collectAnswers(id string, related []string) answerSets {
	req := f.idx.events[id]
	sets := answerSets{refused: map[string]bool{}}
	for _, rid := range related {
		e := f.idx.events[rid]
		if e.Class != model.DecisionValid || e.Decision == nil {
			continue
		}
		switch {
		case e.Decision.Kind == model.DecisionKindAnswer && eligible(req, e):
			sets.valid = append(sets.valid, rid)
		case e.AuthorID == req.AuthorID && e.Decision.Outcome != nil &&
			e.Decision.Outcome.Result == model.DecisionResultRefused:
			for _, n := range f.refusedNames(e) {
				sets.refused[n] = true
			}
		}
	}
	for _, v := range sets.valid { // related is in event id order
		if !sets.refused[v] {
			sets.active = append(sets.active, v)
		}
	}
	return sets
}

// refusedNames returns the canonical ids of the answers a refused outcome
// names; an outcome with no seen_answers names its answer.
func (f *folder) refusedNames(outcome Event) []string {
	o := outcome.Decision.Outcome
	names := o.SeenAnswers
	if len(names) == 0 {
		names = []string{o.Answer}
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, f.idx.resolve(n))
	}
	return out
}

// partition splits answers into those that agree with the first one and the
// rest. Answers agree only when option, input value and note all match.
func (f *folder) partition(ids []string) (agree, differ []string) {
	if len(ids) == 0 {
		return nil, nil
	}
	first := f.answerValue(ids[0])
	for _, id := range ids {
		if f.answerValue(id) == first {
			agree = append(agree, id)
		} else {
			differ = append(differ, id)
		}
	}
	return agree, differ
}
