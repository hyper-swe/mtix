// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/oklog/ulid/v2"

	"github.com/hyper-swe/mtix/internal/model"
)

// historyScope names the tasks a write may change: the task, and with
// cascade its descendants. Actor is the explicit author, or empty to resolve
// the default author as emitted events do (MTIX-24). Origin defaults to local.
type historyScope struct {
	id      string
	cascade bool
	all     bool // every task: a bulk import
	actor   string
	origin  model.HistoryOrigin
	eventID string // the causal sync event, recorded on each entry
}

// historyFieldNames returns the recorded history fields in the order of the
// columns of historySnapshotSQL. Free-text fields are listed so a change is
// noticed; their values are never stored (model.HistoryFieldClassOf).
func historyFieldNames() []string {
	return []string{
		"acceptance", "actual_min", "agent_state", "annotations", "assignee",
		"code_refs", "commit_refs", "defer_until", "deleted_at", "description",
		"estimate_min", "issue_type", "labels", "metadata", "node_type",
		"parent", "priority", "prompt", "status", "title", "weight", "display_path",
	}
}

// historyExcludedColumns names every nodes column that is deliberately not a
// history field, with the reason (MTIX-95.31.19.2). A test requires every
// nodes column to be recorded or listed here, so a new column cannot be
// forgotten. Called, not cached, so no package state holds the list.
func historyExcludedColumns() map[string]string {
	return map[string]string{
		"uid": "identity", "project": "identity", "depth": "derived from id",
		"seq": "derived from id", "creator": "set once at create", "created_at": "bookkeeping timestamp",
		"updated_at": "bookkeeping timestamp", "closed_at": "derived from status", "progress": "derived rollup",
		"content_hash": "derived", "activity": "own stream (ActivityEntry)", "previous_status": "bookkeeping of status",
		"invalidated_at": "derived from status", "invalidated_by": "derived from status",
		"invalidation_reason": "free text, covered by status", "session_id": "bookkeeping", "deleted_by": "covered by deleted_at",
	}
}

// historySnapshotSQL reads the recorded columns of one task, soft-deleted
// rows included so delete and undelete are seen. Primary-key lookup. The
// column order is historyFieldNames, with parent read from parent_id.
const historySnapshotSQL = `SELECT id, COALESCE(uid, ''),
	acceptance, actual_min, agent_state, annotations, assignee,
	code_refs, commit_refs, defer_until, deleted_at, description,
	estimate_min, issue_type, labels, metadata, node_type,
	parent_id, priority, prompt, status, title, weight, id
	FROM nodes WHERE id = ?`

// historySubtreeSQL reads the same columns for the descendants of a task, as
// an id range (">= prefix." and "< prefix/", '/' being '.' + 1) so the primary
// key index serves it; a LIKE pattern would scan the table.
const historySubtreeSQL = `SELECT id, COALESCE(uid, ''),
	acceptance, actual_min, agent_state, annotations, assignee,
	code_refs, commit_refs, defer_until, deleted_at, description,
	estimate_min, issue_type, labels, metadata, node_type,
	parent_id, priority, prompt, status, title, weight, id
	FROM nodes WHERE id >= ? AND id < ?`

// historyAllSQL reads the same columns for every task: the snapshot of a bulk
// import, which rewrites an unbounded set of tasks.
const historyAllSQL = `SELECT id, COALESCE(uid, ''),
	acceptance, actual_min, agent_state, annotations, assignee,
	code_refs, commit_refs, defer_until, deleted_at, description,
	estimate_min, issue_type, labels, metadata, node_type,
	parent_id, priority, prompt, status, title, weight, id
	FROM nodes`

// historySnapshot is the recorded field values of one task.
type historySnapshot struct {
	uid    string
	values map[string]sql.NullString
}

// withHistoryTx runs fn in a write transaction and, in that same transaction,
// appends one task_history entry for every recorded field the write changed
// (MTIX-95.31.19.2). A failing fn or recorder rolls everything back, so a
// failed write leaves no entry, and a write that changes nothing leaves none.
// Other tasks the write reaches (a dependent it unblocks) are added with
// touchHistory before they are written, so one tracker records the whole
// transaction. A nested call joins the outer tracker.
func (s *Store) withHistoryTx(ctx context.Context, scope historyScope, fn func(tx *sql.Tx) error) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.inTxHistory(ctx, tx, scope, func() error { return fn(tx) })
	})
}

// inTxHistory is withHistoryTx for a caller that already holds the write
// transaction (the sync replay, the repair pass). A nested call joins the
// outer tracker.
func (s *Store) inTxHistory(ctx context.Context, tx *sql.Tx, scope historyScope, fn func() error) error {
	if _, ok := s.history.Load(tx); ok {
		if err := s.touchHistory(ctx, tx, scope.id); err != nil {
			return err
		}
		return fn()
	}
	before, err := snapshotTasks(ctx, tx, scope)
	if err != nil {
		return err
	}
	seen := map[string]bool{scope.id: true}
	for id := range before {
		seen[id] = true // a task in the scope's own snapshot is never re-read mid-transaction
	}
	tr := &historyTracker{before: before, seen: seen, origins: map[string]model.HistoryOrigin{}}
	s.history.Store(tx, tr)
	defer s.history.Delete(tx)
	if err := fn(); err != nil {
		return err
	}
	return s.recordHistory(ctx, tx, scope, tr)
}

// snapshotTasks reads the recorded fields of the scoped tasks keyed by node
// id: one primary-key lookup, plus one indexed range read when cascading.
func snapshotTasks(ctx context.Context, tx *sql.Tx, scope historyScope) (map[string]historySnapshot, error) {
	out := map[string]historySnapshot{}
	if scope.all {
		return out, readSnapshots(ctx, tx, out, historyAllSQL)
	}
	if scope.id == "" {
		return out, nil
	}
	if err := readSnapshots(ctx, tx, out, historySnapshotSQL, scope.id); err != nil {
		return nil, err
	}
	if scope.cascade {
		if err := readSnapshots(ctx, tx, out, historySubtreeSQL, scope.id+".", scope.id+"/"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readSnapshots runs one of the fixed snapshot statements into out.
func readSnapshots(ctx context.Context, tx *sql.Tx, out map[string]historySnapshot, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("snapshot history fields: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // read-only cursor, close error carries no data

	names := historyFieldNames()
	for rows.Next() {
		var id string
		snap := historySnapshot{values: map[string]sql.NullString{}}
		vals := make([]sql.NullString, len(names))
		dest := []any{&id, &snap.uid}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("scan history snapshot: %w", err)
		}
		for i, n := range names {
			snap.values[n] = vals[i]
		}
		if snap.uid == "" {
			snap.uid = id // a uid-less legacy task keys on its id; every write that gives it a uid or a new id rekeys its history (task_history_rekey.go)
		}
		out[id] = snap
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate history snapshot: %w", err)
	}
	return nil
}

// recordHistory diffs the scoped tasks against before and appends the entries.
func (s *Store) recordHistory(ctx context.Context, tx *sql.Tx, scope historyScope, tr *historyTracker) error {
	if tr == nil {
		tr = &historyTracker{}
	}
	after, err := snapshotTasks(ctx, tx, scope)
	if err != nil {
		return err
	}
	if err := readTouched(ctx, tx, after, tr); err != nil {
		return err
	}
	for _, m := range tr.moves { // each renumbered subtree, under its new ids
		if err := readSubtree(ctx, tx, after, m.newRoot); err != nil {
			return err
		}
	}
	actor := resolveEmitAuthor(ctx, tx, scope.actor)
	ids := make([]string, 0, len(after))
	for id := range after {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pr := newPairing(tr, scope.all)
	for _, id := range ids {
		prev := pr.before(id, after[id])
		if scope.all && after[id].uid == id && prev.values == nil {
			continue // an import records no task without a uid unless the store itself paired it by a rename
		}
		for _, e := range diffSnapshots(prev, after[id]) {
			e.Actor, e.Timestamp, e.EventID = actor, s.clock().UTC(), scope.eventID
			e.Origin = entryOrigin(scope, tr, id, e.Origin)
			if err := appendHistoryEntry(ctx, tx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// readTouched adds to after the tasks the transaction touched that the scope's
// own snapshot did not cover (a dependent a transition unblocked).
func readTouched(ctx context.Context, tx *sql.Tx, after map[string]historySnapshot, tr *historyTracker) error {
	for id := range tr.seen {
		if _, ok := after[id]; ok || id == "" {
			continue
		}
		if err := readSnapshots(ctx, tx, after, historySnapshotSQL, id); err != nil {
			return err
		}
	}
	return nil
}

// entryOrigin picks the origin of an entry: a task reached by an automatic
// cascade is system, otherwise the scope's origin, otherwise the default.
func entryOrigin(scope historyScope, tr *historyTracker, id string, def model.HistoryOrigin) model.HistoryOrigin {
	if o, ok := tr.originOf(id); ok {
		return o
	}
	if scope.origin != "" {
		return scope.origin
	}
	return def
}

// diffSnapshots returns one change entry per recorded field whose value
// differs; a task absent before (create) counts every non-empty field as set.
func diffSnapshots(before, after historySnapshot) []model.HistoryEntry {
	var out []model.HistoryEntry
	for _, field := range historyFieldNames() {
		old, cur := before.values[field], after.values[field]
		if sameHistoryValue(field, old, cur) {
			continue // unchanged; NULL and '' are the same empty value
		}
		if field == "display_path" && before.values == nil {
			continue // a new task has its path from the start; only a move is a change
		}
		e := model.HistoryEntry{
			TaskUID: after.uid, Origin: model.HistoryOriginLocal,
			Kind: model.HistoryKindChange, Field: field,
		}
		if model.HistoryFieldClassOf(field) == model.HistoryFieldStructured {
			if before.values != nil {
				e.Old = jsonValue(field, old)
			}
			e.New = jsonValue(field, cur)
		}
		out = append(out, e)
	}
	return out
}

// jsonValue renders a column value as JSON text: a string, or null for SQL NULL.
func jsonValue(field string, v sql.NullString) *string {
	text := "null" // the one encoding of every empty value (NULL, '' and an empty list)
	if !sameHistoryValue(field, v, sql.NullString{}) {
		b, err := json.Marshal(v.String)
		if err == nil {
			text = string(b)
		}
	}
	return &text
}

// historyTimeLayout is fixed-width UTC with nanoseconds, so ts sorts as text
// and entries written in one second keep their order.
const historyTimeLayout = "2006-01-02T15:04:05.000000000Z"

// historyAppendSQL appends one entry. ON CONFLICT on the (task_uid, event_id,
// field) index only: a replayed event is a no-op even when the store forgot it
// applied the event (DiscardLocal), while any other constraint violation still
// fails the write.
const historyAppendSQL = `INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))
	ON CONFLICT (task_uid, event_id, field) WHERE event_id IS NOT NULL DO NOTHING`

// appendHistoryEntry mints the entry id and inserts the row.
func appendHistoryEntry(ctx context.Context, tx *sql.Tx, e model.HistoryEntry) error {
	id, err := ulid.New(ulid.Timestamp(e.Timestamp), rand.Reader)
	if err != nil {
		return fmt.Errorf("mint history entry id: %w", err)
	}
	// Append one immutable history row for a changed field of a task.
	if _, err := tx.ExecContext(ctx, historyAppendSQL,
		id.String(), e.TaskUID, e.Timestamp.UTC().Format(historyTimeLayout), e.Actor,
		string(e.Origin), string(e.Kind), e.Field, e.Old, e.New, e.EventID); err != nil {
		return fmt.Errorf("append history entry for %s: %w", e.TaskUID, err)
	}
	return nil
}

// snapshotsByUID indexes snapshots by uid, so a task whose id moved is matched
// to its earlier state. A task keyed by its id (no uid) is not indexed.
func snapshotsByUID(snaps map[string]historySnapshot) map[string]historySnapshot {
	out := make(map[string]historySnapshot, len(snaps))
	for id, snap := range snaps {
		if snap.uid != id {
			out[snap.uid] = snap
		}
	}
	return out
}

// sameHistoryValue reports whether a field kept its value: NULL and ” are
// the same empty value, and for a list field so is an empty JSON list.
func sameHistoryValue(field string, old, cur sql.NullString) bool {
	if old.String == cur.String {
		return true
	}
	switch field {
	case "labels", "annotations", "code_refs", "commit_refs":
		return emptyList(old) && emptyList(cur)
	}
	return false
}

func emptyList(v sql.NullString) bool { return v.String == "" || v.String == "[]" }

// readSubtree adds the task at root and its descendants to out.
func readSubtree(ctx context.Context, tx *sql.Tx, out map[string]historySnapshot, root string) error {
	if err := readSnapshots(ctx, tx, out, historySnapshotSQL, root); err != nil {
		return err
	}
	return readSnapshots(ctx, tx, out, historySubtreeSQL, root+".", root+"/")
}

// pairing finds, for a task now in the store, the state it had before the
// transaction. It never guesses identity, and an import never moves history:
//
//   - A renumber or rename the store itself performs on one task (RenumberSubtree,
//     RenameTo, ImportAs's own mapping, the hub-rejection renumber) is paired
//     through the move it noted.
//   - In a bulk import (scope.all) a task is paired only by its uid, which must be
//     present and unchanged on both sides, wherever its id moved. A task whose uid
//     is new, changed or minted starts fresh, and the history of the task it
//     replaced stays under its old key. Stranding history is acceptable; grafting
//     it onto another task never is.
//   - Outside an import the scope names the one row written, which is the same
//     task before and after.
type pairing struct {
	tr    *historyTracker
	byUID map[string]historySnapshot
	all   bool
}

func newPairing(tr *historyTracker, all bool) *pairing {
	p := &pairing{tr: tr, byUID: map[string]historySnapshot{}, all: all}
	if all {
		p.byUID = snapshotsByUID(tr.before)
	}
	return p
}

func (p *pairing) before(id string, cur historySnapshot) historySnapshot {
	if oldID, ok := p.tr.oldOf(id); ok {
		if b, found := p.tr.before[oldID]; found {
			return b
		}
	}
	if oldID, ok := p.tr.renames[id]; ok {
		if b, found := p.tr.before[oldID]; found {
			return b
		}
	}
	if p.all {
		return p.byUID[cur.uid]
	}
	if b, ok := p.tr.before[id]; ok && b.uid == cur.uid {
		return b
	}
	return historySnapshot{}
}
