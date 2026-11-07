// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// A merge writes columns no sync op carries. Principle B (MTIX-112.17.16.16):
// every value the merge writes is journaled by a real op or named local only,
// by column. mergeColumnTable lists every column of the nodes table and every
// field of an annotation the merge can write, each marked with the op that
// carries it or as local only; the Local only output is computed from it, and
// a test fails when a column of the schema is missing from it (round 3).

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

// mergeColumn is one column of the nodes table, or one field of an annotation
// (named annotations.<field>), with the op that carries its value to a peer.
// A column with no op is local only: differs says when the merge changed it,
// for a changed task (b is the held copy) or a new one (created: b is the
// empty task a create event produces).
type mergeColumn struct {
	name        string
	journaledBy string // "" for a local-only column
	differs     func(p *mergePlanner, b, a *exportNode, created bool) bool
}

// heldChanged marks a column local only when a held task's value changed: a
// new task's value rides on its create event.
func heldChanged(get func(*exportNode) any) func(*mergePlanner, *exportNode, *exportNode, bool) bool {
	return func(_ *mergePlanner, b, a *exportNode, created bool) bool { return !created && get(b) != get(a) }
}

// derivedProgress is the progress a peer holds for a task after the events
// the merge journals: 1.0 for a done task, else what it held (0 for a new one).
func derivedProgress(b, a *exportNode, created bool) float64 {
	switch {
	case a.Status == string(model.StatusDone):
		return 1.0
	case created:
		return 0
	}
	return b.Progress
}

func timeDiffers(b, a string) bool { return a != "" && a != b }

// mergeColumnTable lists every column the merge can write. The times differ
// between replicas by design (a peer stamps its own event time) and are named.
func mergeColumnTable() []mergeColumn {
	op := func(name, by string) mergeColumn { return mergeColumn{name: name, journaledBy: by} }
	local := func(name string, f func(p *mergePlanner, b, a *exportNode, created bool) bool) mergeColumn {
		return mergeColumn{name: name, differs: f}
	}
	return []mergeColumn{
		op("id", "create_node"),
		// Structural columns: the create op sets them once; no op changes them
		// afterwards, so a change to a held task stays local (principle F).
		local("parent_id", heldChanged(func(n *exportNode) any { return n.ParentID })),
		local("depth", heldChanged(func(n *exportNode) any { return n.Depth })),
		local("seq", heldChanged(func(n *exportNode) any { return n.Seq })),
		local("project", heldChanged(func(n *exportNode) any { return n.Project })),
		local("node_type", heldChanged(func(n *exportNode) any { return n.NodeType })),
		local("uid", heldChanged(func(n *exportNode) any { return n.UID })),
		op("title", "update_field"), op("description", "update_field"), op("prompt", "set_prompt"),
		op("acceptance", "set_acceptance"), op("issue_type", "update_field"), op("priority", "update_field"),
		op("labels", "update_field"), op("assignee", "update_field"), op("status", "transition_status"),
		// The defer op carries a wake time only for a task that ends deferred.
		local("defer_until", func(_ *mergePlanner, b, a *exportNode, _ bool) bool {
			return a.Status != string(model.StatusDeferred) && b.DeferUntil != a.DeferUntil
		}),
		// The delete op carries no time: a peer stamps its own.
		local("deleted_at", func(_ *mergePlanner, b, a *exportNode, _ bool) bool {
			return a.DeletedAt != "" && a.DeletedAt != b.DeletedAt
		}),
		op("content_hash", "derived from the content fields"),
		local("agent_state", func(_ *mergePlanner, b, a *exportNode, created bool) bool {
			return created && b.AgentState != a.AgentState
		}),
		local("creator", func(_ *mergePlanner, b, a *exportNode, created bool) bool { return !created && b.Creator != a.Creator }),
		local("weight", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return b.Weight != a.Weight }),
		local("estimate_min", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return !sameInt(b.EstimateMin, a.EstimateMin) }),
		local("actual_min", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return !sameInt(b.ActualMin, a.ActualMin) }),
		local("code_refs", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return !sameRefs(b.CodeRefs, a.CodeRefs) }),
		local("commit_refs", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return !sameStrings(b.CommitRefs, a.CommitRefs) }),
		local("metadata", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return b.Metadata != a.Metadata }),
		local("session_id", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return b.SessionID != a.SessionID }),
		local("invalidated_at", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return b.InvalidatedAt != a.InvalidatedAt }),
		local("invalidated_by", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return b.InvalidatedBy != a.InvalidatedBy }),
		local("invalidation_reason", func(_ *mergePlanner, b, a *exportNode, _ bool) bool {
			return b.InvalidationReason != a.InvalidationReason
		}),
		local("previous_status", func(p *mergePlanner, b, a *exportNode, _ bool) bool {
			_, derived := p.j.blocked[a.ID] // a peer sets it when the link_dep blocks the task
			return !derived && b.PreviousStatus != a.PreviousStatus
		}),
		local("deleted_by", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return b.DeletedBy != a.DeletedBy }),
		local("activity", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return activityDiffers(b, a) }),
		local("created_at", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return timeDiffers(b.CreatedAt, a.CreatedAt) }),
		local("updated_at", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return timeDiffers(b.UpdatedAt, a.UpdatedAt) }),
		local("closed_at", func(_ *mergePlanner, b, a *exportNode, _ bool) bool { return timeDiffers(b.ClosedAt, a.ClosedAt) }),
		// A peer derives progress: 1.0 when a task is done, else the value it held
		// (0 for a new leaf), and a parent's follows its children.
		local("progress", func(p *mergePlanner, b, a *exportNode, created bool) bool {
			return a.Progress != derivedProgress(b, a, created) && !p.hasLiveChildren(a.ID)
		}),
		op("annotations", "comment"),
		op("annotations.id", "comment"), op("annotations.author", "comment"), op("annotations.text", "comment"),
		local("annotations.created_at", nil),
		local("annotations.resolved", nil),
		local("annotations.addressee", nil),
	}
}

// annotationColumnsLocal names the annotation fields no comment event carries
// that differ in a: a blank annotation is not journaled at all, a new one
// carries neither its resolved state nor its time, and a held one can change
// its resolved state.
func annotationColumnsLocal(b, a *exportNode) []string {
	was := make(map[string]model.Annotation, len(b.Annotations))
	for _, x := range b.Annotations {
		was[x.ID] = x
	}
	var out []string
	for _, x := range a.Annotations {
		old, held := was[x.ID]
		switch {
		case held && old.Resolved != x.Resolved:
			out = append(out, "annotations.resolved")
		case held:
		case x.Text == "":
			out = append(out, "annotations")
		default:
			if x.Addressee != "" {
				out = append(out, "annotations.addressee")
			}
			out = append(out, "annotations.created_at")
			if x.Resolved {
				out = append(out, "annotations.resolved")
			}
		}
	}
	return out
}

func activityDiffers(b, a *exportNode) bool {
	x, errX := json.Marshal(b.Activity)
	y, errY := json.Marshal(a.Activity)
	return errX != nil || errY != nil || string(x) != string(y)
}

// MergeColumnCoverage maps every column the merge can write to the op that
// carries it, or to "local only" (MTIX-112.17.16.16).
func MergeColumnCoverage() map[string]string {
	out := map[string]string{}
	for _, c := range mergeColumnTable() {
		out[c.name] = c.journaledBy
		if c.differs != nil || c.journaledBy == "" {
			out[c.name] = "local only"
		}
	}
	return out
}

// localOnlyColumns lists the columns of a that differ from b and no sync op
// carries, whether the task is new (b is the empty task a create produces) or
// changed. It is computed from mergeColumnTable.
func (p *mergePlanner) localOnlyColumns(b, a *exportNode, created bool) []string {
	var out []string
	for _, c := range mergeColumnTable() {
		if c.differs != nil && c.differs(p, b, a, created) {
			out = append(out, c.name)
		}
	}
	return append(out, annotationColumnsLocal(b, a)...)
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
