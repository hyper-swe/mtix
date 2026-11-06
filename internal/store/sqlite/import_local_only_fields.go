// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// The fields of a task an automatic import can change, by the names the
// local-only record and its notices use (MTIX-112.17.16.16). A field is
// settleable when a sync event carries it: a later event then replaces the
// imported value or sends the current one. Other columns (weight, estimates,
// references, metadata) are named in the notice only: no event carries them.

// fieldsOfEvent names the record fields an event of the sync log touches.
func fieldsOfEvent(op, payload string) []string {
	switch model.OpType(op) {
	case model.OpUpdateField:
		var p model.UpdateFieldPayload
		if json.Unmarshal([]byte(payload), &p) == nil && p.FieldName != "" {
			return []string{p.FieldName}
		}
	case model.OpSetPrompt:
		return []string{"prompt"}
	case model.OpSetAcceptance:
		return []string{"acceptance"}
	case model.OpTransitionStatus:
		return []string{"status", "defer_until"}
	case model.OpDefer:
		return []string{"status", "defer_until"}
	case model.OpClaim, model.OpUnclaim:
		return []string{"assignee", "status"}
	case model.OpDelete:
		return []string{"deleted_at"}
	case model.OpComment:
		return []string{"annotations"}
	case model.OpLinkDep, model.OpUnlinkDep:
		return []string{"dependencies"}
	}
	return nil
}

// settleableField reports whether a sync event can carry the field.
func settleableField(name string) bool {
	switch name {
	case "title", "description", "prompt", "acceptance", "labels", "priority", "issue_type", "assignee",
		"agent_state", "status", "defer_until", "deleted_at", "annotations", "dependencies":
		return true
	}
	return false
}

// SettleableFields keeps, of fields, those a sync event carries.
func SettleableFields(fields []string) []string {
	var out []string
	for _, f := range fields {
		if settleableField(f) {
			out = append(out, f)
		}
	}
	return out
}

// nodeFieldDiffs lists the fields of a that differ from b, in the record's
// names: the journaled columns, and the columns no op carries.
func nodeFieldDiffs(b, a *exportNode) []string {
	var out []string
	add := func(differs bool, name string) {
		if differs {
			out = append(out, name)
		}
	}
	add(b.Title != a.Title, "title")
	add(b.Description != a.Description, "description")
	add(b.Prompt != a.Prompt, "prompt")
	add(b.Acceptance != a.Acceptance, "acceptance")
	add(b.Labels != a.Labels, "labels")
	add(b.Priority != a.Priority, "priority")
	add(b.IssueType != a.IssueType, "issue_type")
	add(b.Assignee != a.Assignee, "assignee")
	add(b.AgentState != a.AgentState, "agent_state")
	add(b.Status != a.Status, "status")
	add(b.DeferUntil != a.DeferUntil, "defer_until")
	add(b.DeletedAt != a.DeletedAt, "deleted_at")
	add(annotationIDSet(b) != annotationIDSet(a), "annotations")
	add(b.Weight != a.Weight, "weight")
	add(!sameInt(b.EstimateMin, a.EstimateMin) || !sameInt(b.ActualMin, a.ActualMin), "estimates")
	add(!sameRefs(b.CodeRefs, a.CodeRefs) || !sameStrings(b.CommitRefs, a.CommitRefs), "references")
	add(b.Metadata != a.Metadata, "metadata")
	return out
}

func annotationIDSet(n *exportNode) string {
	ids := make([]string, 0, len(n.Annotations))
	for _, x := range n.Annotations {
		ids = append(ids, x.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// ReplaceFieldChanges names, per task, the fields an automatic import writes:
// those of the tasks diff lists as updated that differ between the local board
// and the board the import writes, plus "dependencies" for the task of every
// dependency the import adds or removes (MTIX-112.17.16.16). Fields no sync
// event carries are listed too; callers record only SettleableFields.
func ReplaceFieldChanges(local, incoming *ExportData, diff *ReplaceDiff) []LocalOnlyImport {
	byID := make(map[string]*exportNode, len(local.Nodes))
	for i := range local.Nodes {
		byID[local.Nodes[i].ID] = &local.Nodes[i]
	}
	fields := map[string][]string{}
	for i := range incoming.Nodes {
		n := &incoming.Nodes[i]
		if b := byID[n.ID]; b != nil && contains(diff.Updated, n.ID) {
			fields[n.ID] = nodeFieldDiffs(b, n)
		}
	}
	for _, dep := range append(append([]string(nil), diff.DepsAdded...), diff.DepsRemoved...) {
		// "<from> <type> <to>": the link event is the from task's.
		if from, _, ok := strings.Cut(dep, " "); ok {
			fields[from] = mergeFields(fields[from], []string{"dependencies"})
		}
	}
	out := make([]LocalOnlyImport, 0, len(fields))
	for id, f := range fields {
		out = append(out, LocalOnlyImport{ID: id, Fields: mergeFields(nil, f)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
