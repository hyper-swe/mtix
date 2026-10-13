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
	actor   string
	origin  model.HistoryOrigin
}

// historyFieldNames returns the recorded history fields in the order of the
// columns of historySnapshotSQL. Free-text fields are listed so a change is
// noticed; their values are never stored (model.HistoryFieldClassOf).
func historyFieldNames() []string {
	return []string{
		"acceptance", "agent_state", "annotations", "assignee", "defer_until",
		"deleted_at", "description", "issue_type", "labels", "node_type",
		"parent", "priority", "prompt", "status", "title",
	}
}

// historyExcludedColumns names every nodes column that is deliberately not a
// history field, with the reason (MTIX-95.31.19.2). A test requires every
// nodes column to be recorded or listed here, so a new column cannot be
// forgotten. Called, not cached, so no package state holds the list.
func historyExcludedColumns() map[string]string {
	return map[string]string{
		"id": "identity", "uid": "identity", "project": "identity", "depth": "derived from id",
		"seq": "derived from id", "creator": "set once at create", "created_at": "bookkeeping timestamp",
		"updated_at": "bookkeeping timestamp", "closed_at": "derived from status", "progress": "derived rollup",
		"content_hash": "derived", "activity": "own stream (ActivityEntry)", "previous_status": "bookkeeping of status",
		"invalidated_at": "derived from status", "invalidated_by": "derived from status",
		"invalidation_reason": "free text, covered by status", "estimate_min": "A2: effort fields", "actual_min": "A2: effort fields",
		"weight": "A2: effort fields", "code_refs": "A2: reference lists", "commit_refs": "A2: reference lists",
		"metadata": "A2: free-form map", "session_id": "bookkeeping", "deleted_by": "covered by deleted_at",
	}
}

// historySnapshotSQL reads the recorded columns of one task, soft-deleted
// rows included so delete and undelete are seen. Primary-key lookup. The
// column order is historyFieldNames, with parent read from parent_id.
const historySnapshotSQL = `SELECT id, COALESCE(uid, ''),
	acceptance, agent_state, annotations, assignee, defer_until,
	deleted_at, description, issue_type, labels, node_type,
	parent_id, priority, prompt, status, title
	FROM nodes WHERE id = ?`

// historySubtreeSQL reads the same columns for the descendants of a task, as
// an id range (">= prefix." and "< prefix/", '/' being '.' + 1) so the primary
// key index serves it; a LIKE pattern would scan the table.
const historySubtreeSQL = `SELECT id, COALESCE(uid, ''),
	acceptance, agent_state, annotations, assignee, defer_until,
	deleted_at, description, issue_type, labels, node_type,
	parent_id, priority, prompt, status, title
	FROM nodes WHERE id >= ? AND id < ?`

// historySnapshot is the recorded field values of one task.
type historySnapshot struct {
	uid    string
	values map[string]sql.NullString
}

// withHistoryTx runs fn in a write transaction and, in that same transaction,
// appends one task_history entry for every recorded field the write changed
// (MTIX-95.31.19.2). A failing fn or recorder rolls everything back, so a
// failed write leaves no entry, and a write that changes nothing leaves none.
func (s *Store) withHistoryTx(ctx context.Context, scope historyScope, fn func(tx *sql.Tx) error) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		before, err := snapshotTasks(ctx, tx, scope)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		return s.recordHistory(ctx, tx, scope, before)
	})
}

// snapshotTasks reads the recorded fields of the scoped tasks keyed by node
// id: one primary-key lookup, plus one indexed range read when cascading.
func snapshotTasks(ctx context.Context, tx *sql.Tx, scope historyScope) (map[string]historySnapshot, error) {
	out := map[string]historySnapshot{}
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
			snap.uid = id // known limit: a uid-less legacy task keys on its id until A2 rewrites it
		}
		out[id] = snap
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate history snapshot: %w", err)
	}
	return nil
}

// recordHistory diffs the scoped tasks against before and appends the entries.
func (s *Store) recordHistory(ctx context.Context, tx *sql.Tx, scope historyScope, before map[string]historySnapshot) error {
	after, err := snapshotTasks(ctx, tx, scope)
	if err != nil {
		return err
	}
	actor := resolveEmitAuthor(ctx, tx, scope.actor)
	ids := make([]string, 0, len(after))
	for id := range after {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, e := range diffSnapshots(before[id], after[id]) {
			e.Actor, e.Timestamp = actor, s.clock().UTC()
			if scope.origin != "" {
				e.Origin = scope.origin
			}
			if err := appendHistoryEntry(ctx, tx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// diffSnapshots returns one change entry per recorded field whose value
// differs; a task absent before (create) counts every non-empty field as set.
func diffSnapshots(before, after historySnapshot) []model.HistoryEntry {
	var out []model.HistoryEntry
	for _, field := range historyFieldNames() {
		old, cur := before.values[field], after.values[field]
		if old.String == cur.String {
			continue // unchanged; NULL and '' are the same empty value
		}
		e := model.HistoryEntry{
			TaskUID: after.uid, Origin: model.HistoryOriginLocal,
			Kind: model.HistoryKindChange, Field: field,
		}
		if model.HistoryFieldClassOf(field) == model.HistoryFieldStructured {
			if before.values != nil {
				e.Old = jsonValue(old)
			}
			e.New = jsonValue(cur)
		}
		out = append(out, e)
	}
	return out
}

// jsonValue renders a column value as JSON text: a string, or null for SQL NULL.
func jsonValue(v sql.NullString) *string {
	text := "null"
	if v.Valid {
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

// appendHistoryEntry mints the entry id and inserts the row.
func appendHistoryEntry(ctx context.Context, tx *sql.Tx, e model.HistoryEntry) error {
	id, err := ulid.New(ulid.Timestamp(e.Timestamp), rand.Reader)
	if err != nil {
		return fmt.Errorf("mint history entry id: %w", err)
	}
	// Append one immutable history row for a changed field of a task.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`,
		id.String(), e.TaskUID, e.Timestamp.UTC().Format(historyTimeLayout), e.Actor,
		string(e.Origin), string(e.Kind), e.Field, e.Old, e.New, e.EventID); err != nil {
		return fmt.Errorf("append history entry for %s: %w", e.TaskUID, err)
	}
	return nil
}
