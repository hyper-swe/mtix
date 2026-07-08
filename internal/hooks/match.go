// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import "strings"

// Matches reports whether event e satisfies hook h's filters. All present
// fields compose with AND; an empty field is a wildcard, so a hook with only
// `events:` matches broadly (FR-19.2). The via-hook re-trigger guard and the
// per-node rate limit are applied by the dispatcher (loop prevention, 47.7),
// not here — Matches is a pure predicate over the declared config filters.
func (h Hook) Matches(e Event) bool {
	m := h.Match
	if len(m.Events) > 0 && !contains(m.Events, e.Name) {
		return false
	}
	// to-agent filters comment.addressed events by their addressee. For events
	// with no addressee (status.changed, node.created) it is NOT a filter — it
	// is the inbox delivery target — so a "wake opus on a status change" hook
	// still matches. Use `under`/`status-to` to scope those events.
	if m.ToAgent != "" && e.Name == EventCommentAddressed && m.ToAgent != e.ToAgent {
		return false
	}
	if m.FromAgentNot != "" && m.FromAgentNot == e.Author {
		return false
	}
	if m.Under != "" && !underSubtree(m.Under, e.NodeID) {
		return false
	}
	if len(m.StatusTo) > 0 && !contains(m.StatusTo, e.StatusTo) {
		return false
	}
	// A synced event fires only for a hook that explicitly opts in (FR-19 §3).
	if e.Synced && !h.IncludeSynced {
		return false
	}
	return true
}

// MatchingHooks returns the hooks in cfg that match e, preserving config order.
func (cfg Config) MatchingHooks(e Event) []Hook {
	var out []Hook
	for _, h := range cfg.Hooks {
		if h.Matches(e) {
			out = append(out, h)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// underSubtree reports whether nodeID is ancestor itself or a descendant of it,
// using dot-path prefix semantics — "HP-1" covers "HP-1", "HP-1.2", "HP-1.2.3"
// but NOT "HP-12" (the trailing dot prevents the false sibling-prefix match).
func underSubtree(ancestor, nodeID string) bool {
	return nodeID == ancestor || strings.HasPrefix(nodeID, ancestor+".")
}
