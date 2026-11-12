// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
)

// OwnEvent is one journal row of the store being enrolled, as the lost-value
// check reads it (MTIX-112.17.16.18).
type OwnEvent struct {
	NodeID  string
	Op      string
	Status  string
	Payload string
}

// LostLocal is the set of node entities this store's own journal touched on
// which the old store and the hub clone, at the hub's head, differ: the hub moved
// on after this store wrote. Ids and field names only, never content.
type LostLocal struct {
	Diff BoardDiff
}

// IDs returns the node ids, sorted.
func (l LostLocal) IDs() []string { return l.Diff.IDs() }

// OwnEntities lists the node ids that journal events this store originated
// touched: the event's node and, for a link or an unlink, the node it points
// at. An event with status applied was mirrored from the hub when the store
// pulled it, so it is the hub's write and not this store's. The check no longer
// lists op kinds: whatever an own event touched is compared whole
// (MTIX-112.17.16.18, ruling r2 point 1).
func OwnEntities(events []OwnEvent) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, e := range events {
		if e.Status == "applied" {
			continue
		}
		add(e.NodeID)
		var p struct {
			DependsOn string `json:"depends_on_node_id"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) == nil {
			add(p.DependsOn)
		}
	}
	sort.Strings(out)
	return out
}

// FindLostLocal compares the moved-aside store's whole export with the real
// clone's export (the hub at its head), using the board comparison and its
// exclusions, and returns every difference on an entity this store's journal
// touched: a missing row, a deleted node, a changed field, a changed link. A
// list field the clone only adds to (a teammate's comment next to this store's
// own) is not a difference. Nil when there is none. A difference on an entity
// no own event touched is the hub moving on, not something this store wrote.
func FindLostLocal(asideExport, cloneExport []byte, own []string, ev *CloneEvidence) (*LostLocal, error) {
	if len(own) == 0 {
		return nil, nil
	}
	c, err := compareReplicas(asideExport, cloneExport, ev)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, id := range own {
		mine[id] = true
	}
	out := BoardDiff{}
	for id, fields := range c.diff {
		if !mine[id] {
			continue
		}
		for _, f := range fields {
			if c.superset[id+"\x00"+f] {
				continue
			}
			out.add(id, f)
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
	return "enrolled, but the old store and the hub differ on " + fmt.Sprint(len(ids)) + " node(s) this worktree wrote to: " + strings.Join(parts, "; ") +
		". The enrolled store holds the hub's version. The old store " + format.SafeLine(r.AsideDir) + " (backup " + format.SafeLine(r.BackupPath) + ") keeps what it held, which may be your value or an earlier hub value; node ids and field names are in " +
		format.SafeLine(r.LostLocalReport) + ". Look at each one; if you still want a value, re-enter it with an ordinary mtix command and push. mtix doctor warns until you clear the record.\n"
}
