// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
)

// maxHistoryWarningIDs is how many conflicting entry ids a warning names.
const maxHistoryWarningIDs = 3

// historyUnion accumulates the outcome of unioning a file's task history into
// the store (MTIX-95.31.19.9).
type historyUnion struct {
	added, present, legacy, conflicts int
	conflictIDs                       []string
	fileConflicts                     int // entry ids the file repeats with different content
	fileConflictIDs                   []string
}

// uniqueEntries returns the file's history entries, node lists first and then
// the top-level list, with each entry id once. Identical copies collapse to
// one. An id the file repeats with different content is dropped whole, every
// copy, and counted as a conflict, so the outcome never depends on which copy
// comes first. The result is in file order.
func (u *historyUnion) uniqueEntries(data *ExportData) []exportHistoryEntry {
	var all []exportHistoryEntry
	for i := range data.Nodes {
		all = append(all, data.Nodes[i].History...)
	}
	all = append(all, data.History...)
	first := make(map[string]exportHistoryEntry, len(all))
	bad := make(map[string]bool)
	for _, e := range all {
		if f, seen := first[e.ID]; !seen {
			first[e.ID] = e
		} else if !sameHistoryEntry(f, e) {
			bad[e.ID] = true
		}
	}
	var out []exportHistoryEntry
	done := make(map[string]bool, len(first))
	for _, e := range all {
		if done[e.ID] {
			if !bad[e.ID] {
				u.present++ // an identical copy within the file
			}
			continue
		}
		done[e.ID] = true
		if bad[e.ID] {
			u.noteFileConflict(e.ID)
			continue
		}
		out = append(out, e)
	}
	return u.collapseEventKeys(out)
}

// collapseEventKeys handles entries of different ids that share an event key
// (task_uid, event_id, field), which the store holds once: copies of the same
// content collapse to the one with the smallest id, and copies of differing
// content are all dropped and counted as conflicts, so no order of the file
// changes the outcome. Entries without an event id are kept as they are.
func (u *historyUnion) collapseEventKeys(in []exportHistoryEntry) []exportHistoryEntry {
	groups := make(map[[3]string][]exportHistoryEntry)
	for _, e := range in {
		if e.EventID != "" {
			k := [3]string{e.TaskUID, e.EventID, e.Field}
			groups[k] = append(groups[k], e)
		}
	}
	drop := make(map[string]bool)
	for _, g := range groups {
		u.collapseGroup(g, drop)
	}
	out := in[:0:0]
	for _, e := range in {
		if !drop[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// collapseGroup marks in drop the entries of one event-key group that the
// union must not store (see collapseEventKeys).
func (u *historyUnion) collapseGroup(g []exportHistoryEntry, drop map[string]bool) {
	if len(g) < 2 {
		return
	}
	sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
	same := true
	for _, e := range g[1:] {
		same = same && sameEventContent(g[0], e)
	}
	for _, e := range g {
		switch {
		case !same:
			drop[e.ID] = true
			u.noteFileConflict(e.ID)
		case e.ID != g[0].ID:
			drop[e.ID] = true
			u.present++
		}
	}
}

// noteFileConflict counts one entry the file's own entries conflict on.
func (u *historyUnion) noteFileConflict(id string) {
	u.fileConflicts++
	if len(u.fileConflictIDs) < maxHistoryWarningIDs {
		u.fileConflictIDs = append(u.fileConflictIDs, id)
	}
}

// sameEventContent reports whether two entries state the same event, ignoring
// the entry id.
func sameEventContent(a, b exportHistoryEntry) bool {
	a.ID, b.ID = "", ""
	return sameHistoryEntry(a, b)
}

// isLegacyHistoryKey reports whether key is a legacy dot id (a node id such
// as PROJ-1.2) rather than a task uid. History is keyed by uid; an entry
// keyed by a dot id could name a different task on another machine, so the
// union never stores it (ADR-006 A3). A key that is not a node id at all,
// and a uid, is not legacy.
func isLegacyHistoryKey(key string) bool {
	if _, err := uuid.Parse(key); err == nil {
		return false
	}
	return model.ValidateNodeID(key) == nil
}

// add unions one entry. The store's row is only ever read or inserted, never
// updated or deleted.
func (u *historyUnion) add(ctx context.Context, tx *sql.Tx, e exportHistoryEntry) error {
	if isLegacyHistoryKey(e.TaskUID) {
		u.legacy++
		return nil
	}
	local, err := readHistoryEntry(ctx, tx, e.ID)
	switch {
	case err == nil:
		u.countExisting(local, e)
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// Absent by entry id: insert. The unique event key can still match an
	// entry with another id, which is the same event already recorded.
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_history
		(entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TaskUID, e.TS, e.Actor, e.Origin, e.Kind, e.Field, e.Old, e.New, nullIfEmpty(e.EventID))
	if err != nil {
		return fmt.Errorf("import history entry %s: %w", e.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("import history entry %s: %w", e.ID, err)
	}
	if n == 0 {
		u.present++
		return nil
	}
	u.added++
	return nil
}

// countExisting counts an entry whose id the store holds: the same content is
// present, any difference is a conflict and the local row stands.
func (u *historyUnion) countExisting(local, incoming exportHistoryEntry) {
	if sameHistoryEntry(local, incoming) {
		u.present++
		return
	}
	u.conflicts++
	if len(u.conflictIDs) < maxHistoryWarningIDs {
		u.conflictIDs = append(u.conflictIDs, incoming.ID)
	}
}

// readHistoryEntry reads the stored entry with the given id, or sql.ErrNoRows.
func readHistoryEntry(ctx context.Context, tx *sql.Tx, id string) (exportHistoryEntry, error) {
	var e exportHistoryEntry
	var oldV, newV, ev sql.NullString
	// The row the store already holds under this entry id, to compare with the file's.
	err := tx.QueryRowContext(ctx, `SELECT entry_id, task_uid, ts, actor, origin, kind, field,
		old_value, new_value, event_id FROM task_history WHERE entry_id = ?`, id).
		Scan(&e.ID, &e.TaskUID, &e.TS, &e.Actor, &e.Origin, &e.Kind, &e.Field, &oldV, &newV, &ev)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		return e, fmt.Errorf("read history entry %s: %w", id, err)
	}
	if oldV.Valid {
		e.Old = &oldV.String
	}
	if newV.Valid {
		e.New = &newV.String
	}
	e.EventID = ev.String
	return e, nil
}

func sameHistoryEntry(a, b exportHistoryEntry) bool {
	return a.ID == b.ID && a.TaskUID == b.TaskUID && a.TS == b.TS && a.Actor == b.Actor &&
		a.Origin == b.Origin && a.Kind == b.Kind && a.Field == b.Field && a.EventID == b.EventID &&
		sameOptional(a.Old, b.Old) && sameOptional(a.New, b.New)
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// report writes the counts and the warnings onto result. A warning names
// counts and the first few entry ids, never an entry's content.
func (u *historyUnion) report(r *ImportResult) {
	r.HistoryAdded += u.added
	r.HistorySkipped += u.present
	r.HistoryConflicts += u.conflicts + u.fileConflicts
	r.HistorySkippedLegacyKey += u.legacy
	if u.conflicts > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"history: %d entries differ from the local entry with the same id; the local entry was kept (first: %s)",
			u.conflicts, strings.Join(u.conflictIDs, ", ")))
	}
	if u.fileConflicts > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"history: %d entry ids appear more than once within the import file with different content; none of those entries was stored (first: %s)",
			u.fileConflicts, strings.Join(u.fileConflictIDs, ", ")))
	}
	if u.legacy > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"history: %d entries keyed by a legacy task id rather than a task uid were skipped", u.legacy))
	}
}
