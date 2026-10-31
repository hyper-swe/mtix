// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import "github.com/hyper-swe/mtix/internal/model"

// ticketDeleted evaluates the ticket_deleted predicate for a request's
// ticket against the delete set. deleted reports whether the predicate
// holds; byUID reports whether any delete matches an index by uid.
//
// Index 0 is the ticket itself and matches a delete with any cascade value;
// an ancestor index matches only a delete with cascade set. A delete
// matches an index by uid, or by path when the index allows path matching
// and the uids give no reason to distrust the path.
func ticketDeleted(t model.DecisionTicket, deletes []Delete) (deleted, byUID bool) {
	uids := append([]string{t.UID}, t.Ancestors...)
	for i, uid := range uids {
		for _, d := range deletes {
			if i > 0 && !d.Cascade {
				continue
			}
			if uid != "" && d.UID == uid {
				return true, true
			}
			if matchesByPath(t, i, uid, d) {
				deleted = true
			}
		}
	}
	return deleted, false
}

// matchesByPath reports a path match for index i.
func matchesByPath(t model.DecisionTicket, i int, uid string, d Delete) bool {
	if i >= len(t.PathMatch) || i >= len(t.Paths) || !t.PathMatch[i] {
		return false
	}
	path := d.Path
	if path == "" {
		path = d.NodeID
	}
	if path == "" || path != t.Paths[i] {
		return false
	}
	return d.UID == "" || model.IsBackfillUID(d.UID) || uid == "" || model.IsBackfillUID(uid)
}
