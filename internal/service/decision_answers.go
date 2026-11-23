// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"maps"
	"slices"
)

type decisionAnswerValue struct{ option, input, note string }
type decisionAnswers struct {
	valid            map[string]decisionAnswerValue
	refused, applied map[string]bool
	withdrawn        bool
}

func newDecisionAnswers() decisionAnswers {
	return decisionAnswers{valid: make(map[string]decisionAnswerValue), refused: make(map[string]bool), applied: make(map[string]bool)}
}

// All cross-event references pass through the same echo resolution as targets.
func (a *decisionAnswers) collect(r, e *DecisionFoldEvent, s decisionSet) {
	p := e.Payload
	switch {
	case p.Kind == "answer" && p.DecisionAnswer != nil:
		if value, ok := eligibleAnswerValue(r, e); ok {
			a.valid[e.ID] = value
		}
	case e.Author != r.Author:
		return
	case p.Kind == "withdraw" && p.DecisionWithdraw != nil:
		a.withdrawn = true
	case p.Kind == "outcome" && p.DecisionOutcome != nil:
		if p.Result == "refused" {
			for _, id := range p.SeenAnswers {
				a.refused[s.resolve(id)] = true
			}
		} else if p.Result == "applied" && r.Payload.RequiresOutcome {
			a.applied[s.resolve(p.Answer)] = true
		}
	}
}

func eligibleAnswerValue(r, e *DecisionFoldEvent) (decisionAnswerValue, bool) {
	if e.Author == "cli" || e.Author == r.Author || (e.Author != r.To && !slices.Contains(r.Payload.Answerers, e.Author)) {
		return decisionAnswerValue{}, false
	}
	p := e.Payload
	kind := p.Decider.Kind
	if kind != "user" && (kind != "agent" || r.Payload.AnswerableBy != "any") {
		return decisionAnswerValue{}, false
	}
	input, err := NormalizeDecisionText(p.InputValue)
	if err != nil {
		return decisionAnswerValue{}, false
	}
	note, err := NormalizeDecisionText(p.Note)
	if err != nil || ValidateDecisionAnswer(r.Payload, p) != nil {
		return decisionAnswerValue{}, false
	}
	return decisionAnswerValue{option: p.Option, input: input, note: note}, true
}

// Intersect the applied set with V before subtracting X. Even a subsequently
// refused answer can therefore remain pinned.
func (a decisionAnswers) apply(c *DecisionFoldCard) {
	c.RefusedAnswers = slices.Sorted(maps.Keys(a.refused))
	var active, pins []string
	for id := range a.valid {
		if a.applied[id] {
			pins = append(pins, id)
		}
		if !a.refused[id] {
			active = append(active, id)
		}
	}
	if len(pins) > 0 {
		pinned := slices.Min(pins)
		c.State, c.PinnedAnswerID, c.EffectiveAnswer = DecisionApplied, pinned, pinned
		c.OutcomeConflict = a.withdrawn || a.refused[pinned] || !a.agree(pins)
		return
	}
	slices.Sort(active)
	switch {
	case a.withdrawn:
		c.State = DecisionWithdrawn
	case len(active) == 0:
		c.State = DecisionOpen
		c.ReopenedAfterRefusal = len(a.refused) > 0
	case a.agree(active):
		c.State, c.AgreeingAnswers, c.EffectiveAnswer = DecisionAnswered, active, active[0]
	default:
		c.State, c.ConflictingAnswers = DecisionConflicted, active
	}
}

func (a decisionAnswers) agree(ids []string) bool {
	for _, id := range ids[1:] {
		if a.valid[id] != a.valid[ids[0]] {
			return false
		}
	}
	return true
}
