// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// A merge writes columns no sync op carries. Principle B (MTIX-112.17.16.16):
// every value the merge writes is journaled by a real op or named local only,
// by column. These functions name the second kind. Three columns follow from
// journaled ops on a peer and are not named: previous_status (the block a
// link_dep makes), and closed_at and progress when the status changes (a
// transition sets them there); progress of a task with live children follows
// from its children.

func sameInt(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameRefs(a, b []model.CodeRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// localOnlyColumns lists the columns of a that differ from b and no sync op
// carries, whether the task is new (b is the empty task a create produces) or
// changed.
func (p *mergePlanner) localOnlyColumns(b, a *exportNode, created bool) []string {
	var out []string
	add := func(differs bool, name string) {
		if differs {
			out = append(out, name)
		}
	}
	add(b.Weight != a.Weight, "weight")
	add(!sameInt(b.EstimateMin, a.EstimateMin), "estimate_min")
	add(!sameInt(b.ActualMin, a.ActualMin), "actual_min")
	add(!sameRefs(b.CodeRefs, a.CodeRefs), "code_refs")
	add(!sameStrings(b.CommitRefs, a.CommitRefs), "commit_refs")
	add(b.Metadata != a.Metadata, "metadata")
	add(b.SessionID != a.SessionID, "session_id")
	add(b.InvalidatedAt != a.InvalidatedAt || b.InvalidatedBy != a.InvalidatedBy ||
		b.InvalidationReason != a.InvalidationReason, "invalidation")
	add(b.Creator != a.Creator && !created, "creator")
	add(created && b.AgentState != a.AgentState, "agent_state")
	add(annotationsLocalOnly(b, a), "annotations")
	sameStatus := b.Status == a.Status
	add(sameStatus && b.ClosedAt != a.ClosedAt, "closed_at")
	add(sameStatus && !p.deferJournaled(b, a) && b.DeferUntil != a.DeferUntil, "defer_until")
	add(sameStatus && b.Progress != a.Progress && !p.hasLiveChildren(a.ID), "progress")
	return out
}

// annotationsLocalOnly reports whether a differs from b in an annotation no
// comment event carries: a blank one, or the resolved state of one b holds.
func annotationsLocalOnly(b, a *exportNode) bool {
	was := make(map[string]model.Annotation, len(b.Annotations))
	for _, x := range b.Annotations {
		was[x.ID] = x
	}
	for _, x := range a.Annotations {
		old, held := was[x.ID]
		if !held && x.Text == "" {
			return true
		}
		if held && old.Resolved != x.Resolved {
			return true
		}
	}
	return false
}

// deferJournaled reports whether planStateChange writes a defer event for
// the change from b to a: the task ends deferred, newly or with another wake time.
func (p *mergePlanner) deferJournaled(b, a *exportNode) bool {
	return a.Status == string(model.StatusDeferred) && (b.Status != a.Status || b.DeferUntil != a.DeferUntil)
}

// hasLiveChildren reports whether task id has live children, whose progress
// it follows.
func (p *mergePlanner) hasLiveChildren(id string) bool {
	var n int
	// Live children of the task: its progress is derived from them.
	if err := p.tx.QueryRowContext(p.ctx, `SELECT COUNT(*) FROM nodes WHERE parent_id = ? AND deleted_at IS NULL`, id).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// deferPayload is the defer event for a task that ends deferred: the wake time
// when the stored one reads as a time.
func deferPayload(a *exportNode) *model.DeferPayload {
	if a.DeferUntil == "" {
		return &model.DeferPayload{}
	}
	t, err := time.Parse(time.RFC3339, a.DeferUntil)
	if err != nil {
		return &model.DeferPayload{}
	}
	return &model.DeferPayload{Until: &t}
}
