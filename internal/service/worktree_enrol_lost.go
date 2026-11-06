// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OwnEvent is one journal row of the store being enrolled, as the lost-value
// check reads it (MTIX-112.17.16.18).
type OwnEvent struct {
	NodeID  string
	Op      string
	Status  string
	Payload string
}

// OwnField names a node field that a journal event this store originated wrote.
type OwnField struct {
	NodeID string
	Field  string
}

// LostLocal is the set of fields whose value this store wrote itself and which
// the hub clone, at the hub's head, holds differently: a write that lost to
// later hub events. Ids and field names only, never content.
type LostLocal struct {
	Diff BoardDiff
}

// IDs returns the node ids, sorted.
func (l LostLocal) IDs() []string { return l.Diff.IDs() }

// ownOpFields maps an op to the export field names it writes. An op that
// writes no single field (create, delete, links) is absent: a row the hub
// lacks is refused before the swap, so it cannot be lost here.
var ownOpFields = map[string][]string{
	"transition_status": {"status"},
	"claim":             {"assignee", "status"},
	"unclaim":           {"assignee"},
	"defer":             {"status", "defer_until"},
	"comment":           {"annotations"},
	"set_acceptance":    {"acceptance"},
	"set_prompt":        {"prompt"},
}

// OwnFieldsOf lists the fields that journal events this store originated wrote.
// An event with status applied was mirrored from the hub when the store pulled
// it, so it is the hub's write and not this store's.
func OwnFieldsOf(events []OwnEvent) []OwnField {
	seen := map[OwnField]bool{}
	var out []OwnField
	add := func(id, field string) {
		f := OwnField{NodeID: id, Field: field}
		if field != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, e := range events {
		if e.Status == "applied" {
			continue
		}
		if e.Op == "update_field" {
			var p struct {
				FieldName string `json:"field_name"`
			}
			if json.Unmarshal([]byte(e.Payload), &p) == nil {
				add(e.NodeID, p.FieldName)
			}
			continue
		}
		for _, f := range ownOpFields[e.Op] {
			add(e.NodeID, f)
		}
	}
	return out
}

// FindLostLocal compares the moved-aside store's export with the real clone's
// export (the hub at its head) and returns the fields that this store wrote
// itself and that differ; nil when there are none. A difference no journal
// event of this store explains is the hub moving on, not a lost write.
func FindLostLocal(asideExport, cloneExport []byte, own []OwnField) (*LostLocal, error) {
	if len(own) == 0 {
		return nil, nil
	}
	c, err := compareBoards(asideExport, cloneExport, BoardExclusions())
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, o := range own {
		mine[o.NodeID+"\x00"+o.Field] = true
	}
	out := BoardDiff{}
	for id, fields := range c.diff {
		for _, f := range fields {
			if key := id + "\x00" + f; mine[key] {
				out.add(id, f)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &LostLocal{Diff: out}, nil
}

// Notice is the text printed when an enrolment kept a hub value over one this
// store wrote. It names ids and fields, the old store and the report.
func (r *WorktreeEnrolResult) Notice() string {
	if r == nil || r.LostLocal == nil || len(r.LostLocal.Diff) == 0 {
		return ""
	}
	ids := r.LostLocal.IDs()
	var parts []string
	for i, id := range ids {
		if i == boardShown {
			parts = append(parts, fmt.Sprintf("and %d more node(s)", len(ids)-boardShown))
			break
		}
		parts = append(parts, id+" ("+strings.Join(r.LostLocal.Diff[id], ", ")+")")
	}
	sort.Strings(parts[:min(len(parts), boardShown)])
	return "enrolled, but the hub holds a later value for " + fmt.Sprint(len(ids)) + " node(s) than the value this worktree wrote: " + strings.Join(parts, "; ") +
		". The enrolled store holds the hub's value. Your value is still in the old store " + r.AsideDir + " (backup " + r.BackupPath + "); node ids and field names are in " + r.LostLocalReport +
		". If you still want it, re-enter it with an ordinary mtix command and push. mtix doctor warns until you clear the record.\n"
}
