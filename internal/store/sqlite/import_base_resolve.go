// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// BaseResolution is the outcome of resolving a pulled board against the
// board last exchanged (ResolveWithBase).
type BaseResolution struct {
	// Resolved is the board an import writes: the file's tasks, each with
	// the local changes and history the base shows the file never saw, and
	// the local tasks made since the exchange. Its checksum is recomputed.
	Resolved *ExportData
	// Diff compares the local store with Resolved. When Diff.Lossy() the
	// import is refused: Diff.Losses lists, per task, the fields changed on
	// both sides (BothChanged) and every other loss.
	Diff *ReplaceDiff
	// Source names the base the resolution used (ImportBase.Source).
	Source string
	// Kept totals what the import keeps that the file lacks.
	Kept KeptHistory
}

// KeptHistory totals what a resolved import keeps from the local store that
// the pulled board lacks (MTIX-95.31.15.1).
type KeptHistory struct {
	// Annotations and Activity count the local entries merged into tasks.
	Annotations, Activity int
	// Fields counts the field values the older form could not carry that
	// the import keeps (previous_status, deleted_by, estimates, ...).
	Fields int
	// LocalChanges counts the task fields changed locally since the
	// exchange that the file left as they were.
	LocalChanges int
	// LocalTasks counts the tasks made locally since the exchange.
	LocalTasks int
}

// threeWayKeys are the 1.0.0 fields decided per task against the base. The
// rest are derived (node_type, progress, content_hash, updated_at) or follow
// the file (id, uid).
var threeWayKeys = []string{
	"parent_id", "depth", "seq", "project", "title", "description", "prompt", "acceptance", "issue_type",
	"priority", "labels", "status", "assignee", "creator", "agent_state", "weight", "created_at",
	"closed_at", "defer_until", "deleted_at",
}

// contentKeys are the fields content_hash derives from.
var contentKeys = map[string]bool{"title": true, "description": true, "prompt": true, "acceptance": true, "labels": true}

// ResolveWithBase resolves the pulled board file against the local store
// using base, the board the two last exchanged, when that board is in an
// older form (MTIX-95.31.15.1, FR-15.2i). Per task, matched as DiffReplace
// matches them, and per field:
//
//   - local equals base: the file's value is the teammate's change and wins;
//   - file equals base: the local value is a change the file never saw and is
//     kept;
//   - both differ: a conflict, listed in NodeLoss.BothChanged;
//   - what the older form could not carry (annotations, activity and the
//     2.0.0-only fields) is kept: the streams are merged by key, a carried
//     field the file leaves empty takes the local value, and two different
//     values are a conflict (previous_status follows the file's status).
//
// A local task the file lacks and the base never held was made since the
// exchange and is kept. The base holds no dependencies, so a dependency is
// resolved only when its author is certain: one touching a task made locally
// since the exchange is kept from the store, one touching a task new in the
// file is taken from the file; when the other task was renumbered on the other side
// the dependency is still refused (merge import is the way out). Every other dependency that differs, between
// tasks the base held, in either direction, is a loss and refused. So is
// everything else the replace would delete (a task the base held, a different
// task under an id). Nothing is changed in local or file.
// Returns ErrInvalidInput for a missing export or a base that is not in an
// older form.
func ResolveWithBase(local, file *ExportData, base *ImportBase) (*BaseResolution, error) {
	if local == nil || file == nil || base == nil || !base.Older {
		return nil, fmt.Errorf("resolve against the base: need both exports and a base in an older form: %w",
			model.ErrInvalidInput)
	}
	r := &baseResolver{base: base, idx: newReplaceIndex(local, file), conflicts: map[string][]string{},
		done: map[string]bool{}, localNew: map[string]bool{}, fileNew: map[string]bool{}}
	resolved := *file
	resolved.Nodes = append([]exportNode(nil), file.Nodes...)
	at := make(map[string]int, len(file.Nodes))
	for i := range resolved.Nodes {
		at[resolved.Nodes[i].ID] = i
	}
	for i := range local.Nodes {
		l, err := asTheMergeSeesIt(&local.Nodes[i])
		if err != nil {
			return nil, err
		}
		if err := r.place(&resolved, at, l); err != nil {
			return nil, err
		}
	}
	for i := range file.Nodes {
		if n := &file.Nodes[i]; !r.done[n.ID] && r.base.taskFor(n, nil) == nil {
			r.fileNew[n.ID] = true
		}
	}
	// The base holds no dependencies. A difference whose author is certain is resolved
	// here; the file's are written and every other difference is listed below as a loss.
	resolved.Dependencies = r.resolveDependencies(file, local)
	if err := RecomputeExportChecksum(&resolved); err != nil {
		return nil, err
	}
	diff, err := diffReplace(local, &resolved, func(*exportNode, *exportNode) bool { return true })
	if err != nil {
		return nil, err
	}
	for id, keys := range r.conflicts {
		sort.Strings(keys)
		diff.lossOf(id).BothChanged = keys
	}
	listFileOnlyDependencies(diff, r.fileNew)
	diff.sort()
	return &BaseResolution{Resolved: &resolved, Diff: diff, Source: base.Source, Kept: r.kept}, nil
}

// baseResolver carries the state of one ResolveWithBase.
type baseResolver struct {
	base      *ImportBase
	idx       *replaceIndex
	conflicts map[string][]string
	done      map[string]bool
	kept      KeptHistory
	// localNew holds the ids of the local tasks made since the exchange; fileNew the ids
	// of the file's tasks that are new to both the base and the store.
	localNew, fileNew map[string]bool
}

// place resolves local node l into resolved: the merged copy over the file's
// node, or l itself as a task made since the exchange.
func (r *baseResolver) place(resolved *ExportData, at map[string]int, l *exportNode) error {
	in, other := r.idx.match(l)
	switch {
	case other != nil:
		r.seen(other.ID)
		return nil // a different task under the id: DiffReplace lists it
	case in == nil:
		if r.base.taskFor(l, nil) == nil {
			resolved.Nodes = append(resolved.Nodes, *l)
			r.kept.LocalTasks++
			r.localNew[l.ID] = true
		}
		return nil
	}
	if r.done[in.ID] {
		return nil
	}
	r.seen(in.ID)
	merged, keys, err := r.resolveNode(l, keptUIDCopy(l, in), r.base.taskFor(l, in))
	if err != nil {
		return err
	}
	resolved.Nodes[at[in.ID]] = *merged
	if len(keys) > 0 {
		r.conflicts[l.ID] = keys
	}
	return nil
}

// seen records that the file's task id matched a local task.
func (r *baseResolver) seen(id string) { r.done[id] = true }

// resolveDependencies returns the file's dependencies plus the local ones that touch a
// task made since the exchange: only the store can have made those (MTIX-95.31.15.1).
// The file's dependencies of a task new in the file are the teammate's and stay.
func (r *baseResolver) resolveDependencies(file, local *ExportData) []exportDep {
	out := append([]exportDep(nil), file.Dependencies...)
	key := func(d *exportDep) string { return d.FromID + "\x00" + d.ToID + "\x00" + d.DepType }
	held := make(map[string]bool, len(out))
	for i := range out {
		held[key(&out[i])] = true
	}
	for i := range local.Dependencies {
		if d := &local.Dependencies[i]; (r.localNew[d.FromID] || r.localNew[d.ToID]) && !held[key(d)] {
			out = append(out, *d)
		}
	}
	return out
}

// resolveNode returns the file's copy in of local node l with the local
// changes and history the base shows in never saw, and the fields changed on
// both sides. bt is the base's task, nil when the base lacks it.
func (r *baseResolver) resolveNode(l, in *exportNode, bt *BaseTask) (*exportNode, []string, error) {
	lraw, ldig, err := nodeFields(l)
	if err != nil {
		return nil, nil, err
	}
	out, fdig, err := nodeFields(in)
	if err != nil {
		return nil, nil, err
	}
	var conflicts, keptLocal []string
	for _, key := range threeWayKeys {
		switch {
		case ldig[key] == fdig[key]:
		case bt != nil && ldig[key] == bt.Fields[key]: // local unchanged: the teammate's change
		case bt != nil && fdig[key] == bt.Fields[key]: // file unchanged: keep the local change
			setField(out, lraw, key)
			keptLocal = append(keptLocal, key)
		default:
			conflicts = append(conflicts, key)
		}
	}
	conflicts = append(conflicts, r.mergeCarried(out, lraw, fdig, ldig, statusSideOf(ldig, fdig, bt))...)
	merged, err := decodeFields(out)
	if err != nil {
		return nil, nil, err
	}
	r.mergeHistory(merged, l)
	deriveFields(merged, l, in, keptLocal)
	r.kept.LocalChanges += len(keptLocal)
	return merged, conflicts, nil
}

// carriedUnits are the 2.0.0-only fields resolved as one unit each. The three
// invalidation fields describe one fact, so they are taken or kept together.
var carriedUnits = [][]string{
	{"previous_status"}, {"estimate_min"}, {"actual_min"}, {"code_refs"}, {"commit_refs"},
	{"invalidated_at", "invalidated_by", "invalidation_reason"}, {"deleted_by"}, {"metadata"}, {"session_id"},
}

// Which side supplied the node's status (statusSide).
const (
	statusSame  = "same"  // both hold the same status
	statusFile  = "file"  // the local status is the base's: the teammate's change applies
	statusLocal = "local" // the file's status is the base's: the local change is kept
)

// mergeCarried resolves each 2.0.0-only field unit against the file: a unit
// the file leaves empty takes the local value, one the local copy leaves empty
// or holds the same keeps the file's, and two different values are a conflict
// (the first differing key is returned). previous_status records the
// transition into the status, so it goes with the side that supplied the
// status: the file's when the status came from the file (the teammate's
// transition), the store's when the local status is kept, even if the file
// holds a different one. Otherwise it is resolved like any other field. A
// deleted_by is dropped when the file holds the task live.
func (r *baseResolver) mergeCarried(out, lraw map[string]json.RawMessage, fdig, ldig map[string]string, statusSide string) []string {
	var conflicts []string
	for _, unit := range carriedUnits {
		if key := r.mergeUnit(unit, out, lraw, fdig, ldig, statusSide); key != "" {
			conflicts = append(conflicts, key)
		}
	}
	if isEmptyValue("deleted_at", out["deleted_at"]) && !isEmptyValue("deleted_by", out["deleted_by"]) {
		delete(out, "deleted_by") // a teammate's undelete clears who deleted it
	}
	return conflicts
}

// mergeUnit resolves one carried unit into out; it returns the first differing
// key when the unit is a conflict, else "".
func (r *baseResolver) mergeUnit(unit []string, out, lraw map[string]json.RawMessage, fdig, ldig map[string]string,
	statusSide string) string {
	localEmpty, fileEmpty, same := true, true, true
	for _, key := range unit {
		localEmpty = localEmpty && isEmptyValue(key, lraw[key])
		fileEmpty = fileEmpty && isEmptyValue(key, out[key])
		same = same && ldig[key] == fdig[key]
	}
	// previous_status goes with the side that supplied the status: the teammate's
	// transition when the file's, the store's when the local status is kept.
	theirs := unit[0] == "previous_status" && statusSide == statusFile
	ours := unit[0] == "previous_status" && statusSide == statusLocal
	switch {
	case same, theirs:
		// Even when the file holds none (a 0.5.3 teammate's board never does), a stale local
		// previous_status would send a later unblock or restore to a status never left.
		return ""
	case localEmpty && !ours:
		return ""
	case fileEmpty, ours:
		// A different (stale) value in the file belongs to the status the store replaced.
		for _, key := range unit {
			setField(out, lraw, key)
			if !isEmptyValue(key, lraw[key]) {
				r.kept.Fields++
			}
		}
		return ""
	}
	return firstDifferent(unit, ldig, fdig)
}

// firstDifferent returns the first key of unit whose digests differ.
func firstDifferent(unit []string, ldig, fdig map[string]string) string {
	for _, key := range unit {
		if ldig[key] != fdig[key] {
			return key
		}
	}
	return unit[0]
}

// statusSideOf says which side supplied the status of the node: the file when
// the local status is the base's, the local store when the file's is.
func statusSideOf(ldig, fdig map[string]string, bt *BaseTask) string {
	switch {
	case ldig["status"] == fdig["status"]:
		return statusSame
	case bt != nil && ldig["status"] == bt.Fields["status"]:
		return statusFile
	case bt != nil && fdig["status"] == bt.Fields["status"]:
		return statusLocal
	}
	return statusSame
}

// mergeHistory sets merged's annotations and activity to their union with
// l's, counting what the file lacked.
func (r *baseResolver) mergeHistory(merged, l *exportNode) {
	annotations, _ := mergeAnnotations(merged.Annotations, l.Annotations)
	activity, _ := mergeActivity(merged.Activity, l.Activity)
	r.kept.Annotations += len(annotations) - len(merged.Annotations)
	r.kept.Activity += len(activity) - len(merged.Activity)
	merged.Annotations, merged.Activity = annotations, activity
}

// deriveFields sets merged's derived fields: updated_at is the later of the
// two copies, content_hash follows the content, and progress follows the
// status (the fields they derive from came from the side that supplied them).
func deriveFields(merged, l, in *exportNode, keptLocal []string) {
	fromLocal := map[string]bool{}
	for _, k := range keptLocal {
		fromLocal[k] = true
	}
	for k := range contentKeys {
		if fromLocal[k] {
			var labels []string
			if err := json.Unmarshal([]byte(merged.Labels), &labels); err != nil {
				merged.ContentHash = l.ContentHash
			} else {
				merged.ContentHash = model.ComputeContentHash(merged.Title, merged.Description, merged.Prompt,
					merged.Acceptance, labels)
			}
			break
		}
	}
	if fromLocal["status"] {
		merged.Progress = l.Progress
	}
	if _, err := time.Parse(time.RFC3339Nano, l.UpdatedAt); err == nil && olderCopy(in.UpdatedAt, l.UpdatedAt) {
		merged.UpdatedAt = l.UpdatedAt
	}
}

// setField sets key in out to src's value, or removes it when src has none.
func setField(out, src map[string]json.RawMessage, key string) {
	if v, ok := src[key]; ok {
		out[key] = v
		return
	}
	delete(out, key)
}

// decodeFields decodes a node from its encoded fields.
func decodeFields(fields map[string]json.RawMessage) (*exportNode, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode the resolved node: %w", err)
	}
	var n exportNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, fmt.Errorf("decode the resolved node: %w", err)
	}
	return &n, nil
}

// listFileOnlyDependencies lists, as conflicts of the task they start from, the
// dependencies only the file holds, except those of a task new in the file (fileNew). The store may have removed them since the
// exchange, and the base cannot say (it holds no dependencies), so writing the
// file's would silently restore them. A dependency only the store holds is
// already a loss (compareDependencies).
func listFileOnlyDependencies(diff *ReplaceDiff, fileNew map[string]bool) {
	for _, entry := range diff.DepsAdded {
		parts := strings.SplitN(entry, " ", 3) // "<from> <type> <to>"
		if len(parts) != 3 {
			continue
		}
		if fileNew[parts[0]] || fileNew[parts[2]] {
			continue // a task new in the file: the teammate made its dependencies
		}
		loss := diff.lossOf(parts[0])
		loss.BothChanged = append(loss.BothChanged, "dependency "+parts[1]+" "+parts[2]+" (only in the file)")
	}
}
