// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ReplaceDiff describes, node by node, what a replace import of a file
// would change in the node and dependency data of the store it replaces
// (FR-15.2i, MTIX-95.31.2). A replace import deletes every node and
// dependency and writes the file's, so the local data the file lacks is
// lost; Losses lists it. Agents and sessions are runtime state and are not
// compared.
type ReplaceDiff struct {
	// Added are the ids of the nodes only the file holds.
	Added []string
	// Updated are the ids of the nodes both hold whose exported content
	// differs.
	Updated []string
	// Removed are the ids of the nodes only the store holds.
	Removed []string
	// DepsAdded are the dependencies only the file holds, as
	// "<from> <type> <to>".
	DepsAdded []string
	// DepsRemoved are the dependencies only the store holds, as
	// "<from> <type> <to>".
	DepsRemoved []string
	// Losses lists, by node id, the local data the file lacks.
	Losses []NodeLoss
}

// NodeLoss lists what a replace import would delete from one local node
// (FR-15.2i, MTIX-95.31.2).
type NodeLoss struct {
	// NodeID is the local node's id.
	NodeID string
	// WholeNode is true when the file lacks the node itself.
	WholeNode bool
	// SoftDeleted is true when the local node is soft-deleted (context for
	// the reader: a teammate's gc may have purged it).
	SoftDeleted bool
	// Annotations are the ids of the local annotations the file lacks, in
	// local order.
	Annotations []string
	// Unresolved are the ids of the annotations resolved locally that the
	// file holds unresolved: the replace would undo the resolution.
	Unresolved []string
	// Activity counts the local activity entries the file lacks.
	Activity int
	// Fields names, by their export key and sorted, the node fields that
	// hold a value locally and that the file leaves empty, when the file's
	// copy of the node is not known to be current (see DiffReplace). A
	// cleared field in a current copy is a deliberate change, not a loss.
	Fields []string
	// Dependencies are the local dependencies from this node the file
	// lacks, as "<type> <to>".
	Dependencies []string
	// DifferentTask is true when the file holds a different task under
	// the node's id: both carry a uid and the uids differ (MTIX-95.31.4).
	// The replace would delete the whole local task, so the node's other
	// losses are not listed. LocalTitle and FileTitle name the two tasks.
	DifferentTask bool
	// SameTaskAtUpgrade is true when the file holds the node's id under
	// another uid, the two count as one task only because a uid was
	// assigned when a clone upgraded from before uids were shared
	// (differentIdentity), and their titles differ (MTIX-95.31.6): they may
	// be two tasks that rule cannot tell apart, and the file's copy would
	// replace the local task. LocalTitle and FileTitle name the two copies.
	SameTaskAtUpgrade bool
	// DifferentTitleNoUID is true when the file holds the node's id with
	// another title and one of the two has no uid to compare
	// (MTIX-95.31.9): a board written before uids were shared, or a local
	// task imported from one. Taken for a different task, the replace
	// would delete the whole local task; a merge renumbers it.
	DifferentTitleNoUID bool
	LocalTitle          string
	FileTitle           string
}

// Lossy reports whether the replace would delete any local data.
func (d *ReplaceDiff) Lossy() bool { return len(d.Losses) > 0 }

// lossy reports whether the node loses anything, or may lose the whole
// task (SameTaskAtUpgrade, MTIX-95.31.6; DifferentTitleNoUID,
// MTIX-95.31.9).
func (l *NodeLoss) lossy() bool {
	return l.WholeNode || len(l.Annotations) > 0 || len(l.Unresolved) > 0 ||
		l.Activity > 0 || len(l.Fields) > 0 || len(l.Dependencies) > 0 || l.SameTaskAtUpgrade ||
		l.DifferentTitleNoUID
}

// markSameTaskAtUpgrade flags, with both titles, a local node l that match
// took for one task with in, the file's node under l's id, although their
// titles differ: DifferentTitleNoUID when either has no uid to compare
// (differentTitleNoUID, MTIX-95.31.9: a merge takes them for different
// tasks), and SameTaskAtUpgrade when both carry uids that differ, so only
// the upgrade rule of differentIdentity made them one (MTIX-95.31.6).
func markSameTaskAtUpgrade(loss *NodeLoss, l, in *exportNode) {
	switch {
	case differentTitleNoUID(l, in):
		loss.DifferentTitleNoUID, loss.LocalTitle, loss.FileTitle = true, l.Title, in.Title
	case l.UID != in.UID && l.Title != in.Title:
		loss.SameTaskAtUpgrade, loss.LocalTitle, loss.FileTitle = true, l.Title, in.Title
	}
}

// DiffReplace compares a store's own export (local) with the export a
// replace import would write (file) and reports the nodes and dependencies
// the file adds, updates and removes, and every piece of local node and
// dependency data the file lacks (FR-15.2i, MTIX-95.31.2):
//
//   - always a loss: a node, an annotation (by id), an annotation's
//     resolution, an activity entry (by the key merge import uses: id,
//     type, author, text and time) and a dependency (from, to, type);
//   - always a loss of the whole local task: a node whose id the file
//     gives to a different task (differentTask: another uid and another
//     identity; DifferentTask, MTIX-95.31.4). A local node whose uid the
//     file holds under another id is the same task, renumbered by another
//     clone: it is compared with that copy, and its dependencies are read
//     at the ids the file gives its ends;
//   - possibly a loss of the whole local task: a node whose id the file
//     holds under another uid that differentTask takes for the same task
//     (a uid assigned at upgrade) while the titles differ
//     (SameTaskAtUpgrade, MTIX-95.31.6): the two may be different tasks
//     created in the same second whose create event at least one clone's
//     event log lacks (for example, created before 0.2); and a node the
//     file holds under its id with another title when either has no uid
//     to compare (DifferentTitleNoUID, MTIX-95.31.9), which a merge takes
//     for a different task;
//   - a loss only when the file's copy of the node is not known to be
//     current: a non-empty field value the file leaves empty, except a
//     marked backfill uid, which the replace keeps (keptUIDCopy,
//     MTIX-95.31.9). The copy is
//     current when the file carries activity (schema 2.0.0 or later),
//     holds every local activity entry of the node, and its updated_at is
//     not older than the local one. Activity is append-only, and the
//     writes that record none (mtix update, mtix delete) still move
//     updated_at, so such a copy has seen the local changes, and a field
//     it cleared was cleared on purpose (unclaim, reopen, undefer,
//     undelete). A 1.x file carries no activity and is never current. A
//     skewed clock can make a current copy look older: that refuses, the
//     safe side.
//
// It is a pure comparison: neither export is changed.
func DiffReplace(local, file *ExportData) (*ReplaceDiff, error) {
	if local == nil || file == nil {
		return nil, fmt.Errorf("compare exports for a replace import: missing export: %w", model.ErrInvalidInput)
	}
	fileCarriesActivity := carriesNodeColumns(file.SchemaVersion)
	idx := newReplaceIndex(local, file)
	matched := make(map[*exportNode]bool, len(file.Nodes))
	sameTaskAt := make(map[string]string, len(local.Nodes)) // local id -> the file's id of that task
	diff := &ReplaceDiff{}
	for i := range local.Nodes {
		l := &local.Nodes[i]
		in, other := idx.match(l)
		switch {
		case other != nil: // MTIX-95.31.4: a replace deletes the local task
			diff.Updated = append(diff.Updated, l.ID)
			diff.Losses = append(diff.Losses, NodeLoss{NodeID: l.ID, SoftDeleted: l.DeletedAt != "",
				DifferentTask: true, LocalTitle: l.Title, FileTitle: other.Title})
			continue
		case in == nil:
			diff.Removed = append(diff.Removed, l.ID)
			diff.Losses = append(diff.Losses, NodeLoss{NodeID: l.ID, WholeNode: true, SoftDeleted: l.DeletedAt != ""})
			continue
		}
		matched[in], sameTaskAt[l.ID] = true, in.ID
		loss, changed, err := compareReplacedNode(l, keptUIDCopy(l, in), fileCarriesActivity) // MTIX-95.31.9
		if err != nil {
			return nil, err
		}
		markSameTaskAtUpgrade(&loss, l, in) // MTIX-95.31.6
		if changed {
			diff.Updated = append(diff.Updated, l.ID)
		}
		if loss.lossy() {
			diff.Losses = append(diff.Losses, loss)
		}
	}
	for i := range file.Nodes {
		if !matched[&file.Nodes[i]] {
			diff.Added = append(diff.Added, file.Nodes[i].ID)
		}
	}
	diff.compareDependencies(local.Dependencies, file.Dependencies, sameTaskAt)
	diff.sort()
	return diff, nil
}

// replaceIndex finds, for a local node, the file's copy of the same task
// (MTIX-95.31.4).
type replaceIndex struct {
	fileByID, fileByUID map[string]*exportNode
	localUIDs           map[string]bool
}

// newReplaceIndex indexes the file's nodes by id and by uid, and the uids
// the local store holds.
func newReplaceIndex(local, file *ExportData) *replaceIndex {
	idx := &replaceIndex{
		fileByID:  make(map[string]*exportNode, len(file.Nodes)),
		fileByUID: make(map[string]*exportNode, len(file.Nodes)),
		localUIDs: make(map[string]bool, len(local.Nodes)),
	}
	for i := range file.Nodes {
		n := &file.Nodes[i]
		idx.fileByID[n.ID] = n
		if n.UID != "" {
			idx.fileByUID[n.UID] = n
		}
	}
	for i := range local.Nodes {
		if uid := local.Nodes[i].UID; uid != "" {
			idx.localUIDs[uid] = true
		}
	}
	return idx
}

// match returns the file's copy of local node l, or the file's different
// task under l's id, or neither when the file lacks l (MTIX-95.31.4). The
// file's node with l's uid is l's copy wherever it sits: another clone may
// have renumbered the task, a move rather than a loss. Otherwise the node
// under l's id is l's copy unless it is a different task: another local
// task, which a clone moved there (its uid is held locally), or one
// differentTask tells apart. Either way a merge renumbers l, with
// confirmation, so the refusal lists it as a different task.
func (x *replaceIndex) match(l *exportNode) (same, different *exportNode) {
	f := x.fileByID[l.ID]
	if f != nil && l.UID != "" && f.UID == l.UID {
		return f, nil // the same task under the same id
	}
	if moved := x.fileByUID[l.UID]; l.UID != "" && moved != nil {
		return moved, nil
	}
	switch {
	case f == nil:
		return nil, nil
	case f.UID != "" && x.localUIDs[f.UID], differentTask(l, f):
		return nil, f
	}
	return f, nil
}

// sort orders every list of the diff, the losses by node id.
func (d *ReplaceDiff) sort() {
	for _, list := range [][]string{d.Added, d.Updated, d.Removed, d.DepsAdded, d.DepsRemoved} {
		sort.Strings(list)
	}
	sort.Slice(d.Losses, func(i, j int) bool { return d.Losses[i].NodeID < d.Losses[j].NodeID })
}

// depLabel names a dependency as "<type> <to>", the part a node's loss
// lists under the node it starts from.
func depLabel(d *exportDep) string { return d.DepType + " " + d.ToID }

// compareDependencies records the dependencies only the file holds and
// those only the store holds; each of the latter is a loss of the node it
// starts from. A local dependency's ends are read as the file's ids of the
// same tasks (sameTaskAt), so a dependency on a task the file holds under
// another id is held (MTIX-95.31.4).
func (d *ReplaceDiff) compareDependencies(local, file []exportDep, sameTaskAt map[string]string) {
	key := func(dep *exportDep) string { return dep.FromID + "\x00" + dep.ToID + "\x00" + dep.DepType }
	inFile := func(id string) string {
		if moved, ok := sameTaskAt[id]; ok {
			return moved
		}
		return id
	}
	held := make(map[string]bool, len(file))
	for i := range file {
		held[key(&file[i])] = true
	}
	localHeld := make(map[string]bool, len(local))
	for i := range local {
		dep := &local[i]
		asFile := exportDep{FromID: inFile(dep.FromID), ToID: inFile(dep.ToID), DepType: dep.DepType}
		localHeld[key(&asFile)] = true
		if held[key(&asFile)] {
			continue
		}
		d.DepsRemoved = append(d.DepsRemoved, dep.FromID+" "+depLabel(dep))
		loss := d.lossOf(dep.FromID)
		loss.Dependencies = append(loss.Dependencies, depLabel(dep))
	}
	for i := range file {
		if dep := &file[i]; !localHeld[key(dep)] {
			d.DepsAdded = append(d.DepsAdded, dep.FromID+" "+depLabel(dep))
		}
	}
}

// lossOf returns the loss recorded for node id, adding an empty one.
func (d *ReplaceDiff) lossOf(id string) *NodeLoss {
	for i := range d.Losses {
		if d.Losses[i].NodeID == id {
			return &d.Losses[i]
		}
	}
	d.Losses = append(d.Losses, NodeLoss{NodeID: id})
	return &d.Losses[len(d.Losses)-1]
}

// compareReplacedNode compares the local copy of a node with the file's and
// returns what replacing it would lose, and whether the two differ. Cleared
// fields count only when the file's copy is not current: the file carries
// no activity, lacks a local activity entry, or its copy is older.
func compareReplacedNode(local, in *exportNode, fileCarriesActivity bool) (NodeLoss, bool, error) {
	loss := NodeLoss{NodeID: local.ID, SoftDeleted: local.DeletedAt != ""}
	localJSON, err := json.Marshal(local)
	if err != nil {
		return loss, false, fmt.Errorf("encode local node %s: %w", local.ID, err)
	}
	inJSON, err := json.Marshal(in)
	if err != nil {
		return loss, false, fmt.Errorf("encode node %s of the file: %w", in.ID, err)
	}
	loss.Annotations, loss.Unresolved = lostAnnotations(local.Annotations, in.Annotations)
	loss.Activity = lostActivity(local.Activity, in.Activity)
	if !copyIsCurrent(local, in, fileCarriesActivity) {
		loss.Fields, err = blankedFields(localJSON, inJSON)
		if err != nil {
			return loss, false, fmt.Errorf("compare node %s: %w", local.ID, err)
		}
	}
	return loss, !bytes.Equal(localJSON, inJSON), nil
}

// copyIsCurrent reports whether the file's copy of a node has seen every
// local change (FR-15.2i): the file carries activity (schema 2.0.0 or
// later), the copy holds every local activity entry of the node, and its
// updated_at is not older than the local one. DiffReplace lists a field the
// copy leaves empty as a loss only when it is not current, and merge keeps
// such a field by the same rule (MTIX-95.31.4).
func copyIsCurrent(local, in *exportNode, fileCarriesActivity bool) bool {
	return fileCarriesActivity && lostActivity(local.Activity, in.Activity) == 0 && !olderCopy(in.UpdatedAt, local.UpdatedAt)
}

// olderCopy reports whether the file's updated_at is older than the local
// one, or cannot be read (a copy of unknown age is never current).
func olderCopy(fileUpdatedAt, localUpdatedAt string) bool {
	fileTime, fileErr := time.Parse(time.RFC3339Nano, fileUpdatedAt)
	localTime, localErr := time.Parse(time.RFC3339Nano, localUpdatedAt)
	if fileErr != nil || localErr != nil {
		return true
	}
	return fileTime.Before(localTime)
}

// notComparedAsField reports the node keys not compared as field values:
// annotations and activity are compared entry by entry, and node_type is
// derived from depth on import, so a file cannot lose it.
func notComparedAsField(key string) bool {
	switch key {
	case "annotations", "activity", "node_type":
		return true
	}
	return false
}

// blankedFields returns, sorted, the keys of the fields that hold a value in
// the local node's JSON and are empty or absent in the file's.
func blankedFields(localJSON, inJSON []byte) ([]string, error) {
	var local, in map[string]json.RawMessage
	if err := json.Unmarshal(localJSON, &local); err != nil {
		return nil, fmt.Errorf("decode local node: %w", err)
	}
	if err := json.Unmarshal(inJSON, &in); err != nil {
		return nil, fmt.Errorf("decode node of the file: %w", err)
	}
	var fields []string
	for key, value := range local {
		if notComparedAsField(key) || isEmptyValue(key, value) {
			continue
		}
		if isEmptyValue(key, in[key]) {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields, nil
}

// isEmptyValue reports whether an encoded field value holds nothing: absent,
// null, an empty string, list or object, or, for labels and metadata, which
// hold JSON text in a string, the text of one. Numbers and booleans always
// hold a value.
func isEmptyValue(key string, value json.RawMessage) bool {
	switch string(value) {
	case "", "null", `""`, "[]", "{}":
		return true
	case `"[]"`, `"{}"`, `"null"`:
		return key == "labels" || key == "metadata"
	}
	return false
}

// lostAnnotations returns the ids of the local annotations the file lacks,
// keyed as merge import keys them (annotationKey), and the ids of those it
// holds unresolved while the local copy is resolved.
func lostAnnotations(local, in []model.Annotation) (missing, unresolved []string) {
	held := make(map[streamKey]bool, len(in))
	for i := range in {
		held[annotationKey(&in[i])] = in[i].Resolved
	}
	for i := range local {
		resolved, ok := held[annotationKey(&local[i])]
		switch {
		case !ok:
			missing = append(missing, annotationLabel(&local[i]))
		case local[i].Resolved && !resolved:
			unresolved = append(unresolved, annotationLabel(&local[i]))
		}
	}
	return missing, unresolved
}

// annotationLabel names an annotation for a reader: its id, or its author
// and time when it has none.
func annotationLabel(a *model.Annotation) string {
	if a.ID != "" {
		return a.ID
	}
	return "by " + a.Author + " at " + a.CreatedAt.UTC().Format(time.RFC3339)
}

// lostActivity counts the local activity entries the file lacks, keyed as
// merge import keys them (activityKey).
func lostActivity(local, in []model.ActivityEntry) int {
	held := make(map[streamKey]bool, len(in))
	for i := range in {
		held[activityKey(&in[i])] = true
	}
	lost := 0
	for i := range local {
		if !held[activityKey(&local[i])] {
			lost++
		}
	}
	return lost
}
