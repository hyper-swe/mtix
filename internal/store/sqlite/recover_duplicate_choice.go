// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "fmt"

func newSalvageState(mirror *mirrorSource) *salvageState {
	return &salvageState{
		ownEntry: map[int64]bool{}, keptAt: map[string]int64{}, reported: map[int64]bool{},
		mirror: mirror, mirrors: map[string]*exportNode{}, queued: map[int64]bool{},
		firstMisread: map[int64]salvageKey{}, choices: map[string]string{},
		discardedByID: map[string][]int{},
		columns:       map[string][]*unreadableColumnError{},
	}
}

// duplicateMirror has no JSON-column schema floor or UID mismatch filter:
// UID and content hash are the evidence used to choose the row itself.
func (st *salvageState) duplicateMirror(id string) *exportNode {
	if n, checked := st.mirrors[id]; checked {
		return n
	}
	var n *exportNode
	if st.mirror != nil && st.mirror.data != nil {
		positions := st.mirror.byID[id]
		if len(positions) == 1 {
			candidate := &st.mirror.data.Nodes[positions[0]]
			if validateNodeTimes(candidate) == nil {
				n = candidate
			}
		}
	}
	st.mirrors[id] = n
	return n
}

type duplicateMatch struct{ uid, hash bool }

type misreadNote struct {
	key    salvageKey
	nodeID string
}

func matchDuplicate(row exportNode, mirror *exportNode) duplicateMatch {
	if mirror == nil {
		return duplicateMatch{}
	}
	return duplicateMatch{
		uid:  row.UID != "" && mirror.UID != "" && row.UID == mirror.UID,
		hash: row.ContentHash != "" && mirror.ContentHash != "" && row.ContentHash == mirror.ContentHash,
	}
}

func duplicateIsBetter(candidate, current duplicateMatch) bool {
	if candidate.uid != current.uid {
		return candidate.uid
	}
	return candidate.hash && !current.hash
}

// A strict comparison leaves ties and the no-match fallback in their old order.
func chooseDuplicateRow(out *dbSalvage, st *salvageState, row salvagedRow) bool {
	b := row.node.ID
	current := keptDuplicateRow(out, st, b)
	mirror := st.duplicateMirror(b)
	if !duplicateIsBetter(matchDuplicate(row.node, mirror), matchDuplicate(current.node, mirror)) {
		queueDuplicateRow(st, row)
		return false
	}
	queueDuplicateRow(st, current)
	out.nodes[b] = row.node
	st.keptAt[b] = row.key.rowid
	delete(st.ownEntry, current.key.rowid)
	if len(row.cols) > 0 {
		st.columns[b] = row.cols
	} else {
		delete(st.columns, b)
	}
	return true
}

func keptDuplicateRow(out *dbSalvage, st *salvageState, id string) salvagedRow {
	return salvagedRow{key: salvageKey{id: id, rowid: st.keptAt[id]}, node: out.nodes[id], cols: st.columns[id]}
}

func queueDuplicateRow(st *salvageState, row salvagedRow) {
	if st.queued[row.key.rowid] {
		return
	}
	st.queued[row.key.rowid] = true
	st.discardedByID[row.node.ID] = append(st.discardedByID[row.node.ID], len(st.discarded))
	st.discarded = append(st.discarded, row)
}

// Render after selection so three-row groups never name an intermediate winner.
func finishDuplicateChoices(out *dbSalvage, st *salvageState, res *RecoverResult) {
	for _, id := range res.RecoveredIDs {
		out.unreadable = append(out.unreadable, st.columns[id]...)
	}
	for _, key := range st.repeated {
		res.Notes = append(res.Notes, repeatedChoiceNote(st, key))
	}
	for _, row := range st.misreadNotes {
		res.Notes = append(res.Notes, misreadChoiceNote(st, row))
	}
	for _, row := range st.discarded {
		b := row.node.ID
		if _, set := st.choices[b]; !set {
			st.choices[b] = duplicateChoiceReason(st, b, out.nodes[b])
		}
		reportDuplicateRow(st, res, row)
	}
}

func duplicateChoiceReason(st *salvageState, id string, winner exportNode) string {
	mirror := st.duplicateMirror(id)
	best := matchDuplicate(winner, mirror)
	reason := "first-kept"
	if best.uid {
		reason = "uid-match"
	} else if best.hash {
		reason = "hash-match"
	}
	hashBreaksUIDTie, tied := false, false
	for _, index := range st.discardedByID[id] {
		row := st.discarded[index]
		other := matchDuplicate(row.node, mirror)
		hashBreaksUIDTie = hashBreaksUIDTie || best.uid && best.hash && other.uid && !other.hash
		tied = tied || best == other
	}
	if hashBreaksUIDTie {
		reason = "uid-match then hash-match"
	}
	if tied && (best.uid || best.hash) {
		reason += " then first-kept among equal matches"
	}
	return reason
}

func repeatedChoiceNote(st *salvageState, key salvageKey) string {
	if st.keptAt[key.id] == key.rowid {
		return fmt.Sprintf("node %s: a second index entry for %s points at the same row, which is salvaged once", key.id, key.id)
	}
	return fmt.Sprintf("node %s: a second index entry for %s points at the same discarded duplicate row (see the note on %s)", key.id, key.id, key.id)
}

func misreadChoiceNote(st *salvageState, row misreadNote) string {
	a, b := row.key.id, row.nodeID
	note := fmt.Sprintf("node %s: the index entry for %s points at the row of %s; ", a, a, b)
	switch {
	case st.ownEntry[row.key.rowid]:
		note += fmt.Sprintf("the entry of %s reaches that row, so it is not salvaged again", b)
	case st.keptAt[b] == row.key.rowid && st.firstMisread[row.key.rowid] == row.key:
		note += fmt.Sprintf("no entry of %s reaches that row, so it is salvaged once, as %s", b, b)
	case st.keptAt[b] == row.key.rowid:
		note += fmt.Sprintf("a row is already salvaged as %s, so this one is not salvaged again", b)
	default:
		note += fmt.Sprintf("the table holds two rows with id %s, so it is not salvaged (see the note on %s)", b, b)
	}
	return note
}
