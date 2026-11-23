// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// Build the graph only from rule-1-valid requests. Higher priority terminal
// states still have outgoing supersedes edges.
func applySupersession(requests map[string]*DecisionFoldEvent, cards map[string]DecisionFoldCard, set decisionSet) {
	edges, reverse := make(map[string][]string), make(map[string][]string)
	for id, r := range requests {
		if cards[id].State == DecisionInvalid {
			continue
		}
		for _, ref := range r.Payload.Supersedes {
			target := set.resolve(ref)
			prior := requests[target]
			if target == id || prior == nil || cards[target].State == DecisionInvalid || !sameDecisionScope(r, prior) {
				continue
			}
			edges[id] = append(edges[id], target)
			reverse[target] = append(reverse[target], id)
		}
	}
	components := decisionComponents(edges, reverse)
	for source, targets := range edges {
		for _, target := range targets {
			c := cards[target]
			if components[source] == components[target] {
				c.KeyConflict = true
			} else if c.State != DecisionApplied && c.State != DecisionWithdrawn {
				c.State = DecisionSuperseded
				c.AgreeingAnswers, c.ConflictingAnswers, c.EffectiveAnswer = nil, nil, ""
				c.ReopenedAfterRefusal = false
			}
			cards[target] = c
		}
	}
	flagConcurrentRequests(requests, cards)
}

func sameDecisionScope(a, b *DecisionFoldEvent) bool {
	return a.Author == b.Author && a.Payload.Key == b.Payload.Key && (a.Payload.Key != "" || a.Payload.Ticket.UID == b.Payload.Ticket.UID)
}

// Kosaraju's two DFS passes label each strongly connected component with one
// representative. Only equality of labels is used, so map traversal order is immaterial.
func decisionComponents(edges, reverse map[string][]string) map[string]string {
	var finish []string
	seen := make(map[string]bool)
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, target := range edges[id] {
			visit(target)
		}
		finish = append(finish, id)
	}
	for id := range edges {
		visit(id)
	}
	component := make(map[string]string)
	var assign func(string, string)
	assign = func(id, root string) {
		if _, ok := component[id]; ok {
			return
		}
		component[id] = root
		for _, source := range reverse[id] {
			assign(source, root)
		}
	}
	for i := len(finish) - 1; i >= 0; i-- {
		assign(finish[i], finish[i])
	}
	return component
}

// Only requests still open after all seven rules compete for an explicit key.
// Lead ruling (rule 4): "two open requests" means state open only, not not-yet-closed.
func flagConcurrentRequests(requests map[string]*DecisionFoldEvent, cards map[string]DecisionFoldCard) {
	open := make(map[decisionGroup][]string)
	for id, r := range requests {
		if cards[id].State == DecisionOpen && r.Payload.Key != "" {
			group := decisionGroup{r.Author, r.Payload.Key}
			open[group] = append(open[group], id)
		}
	}
	for _, ids := range open {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			c := cards[id]
			c.KeyConflict = true
			cards[id] = c
		}
	}
}
