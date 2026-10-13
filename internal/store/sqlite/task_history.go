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
// the default author as emitted events do (MTIX-24).
type historyScope struct {
	id      string
	cascade bool
	actor   string
}

// historyColumns maps each recorded history field to its nodes column.
// Free-text fields are listed so a change is noticed; their values are never
// stored (model.HistoryFieldClassOf).
var historyColumns = map[string]string{
	"title": "title", "description": "description", "prompt": "prompt",
	"acceptance": "acceptance", "annotations": "annotations",
	"status": "status", "assignee": "assignee", "priority": "priority",
	"labels": "labels", "node_type": "node_type", "issue_type": "issue_type",
	"parent": "parent_id", "defer_until": "defer_until",
	"deleted_at": "deleted_at", "agent_state": "agent_state",
}

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

// snapshotTasks reads the recorded fields of the scoped tasks, soft-deleted
// ones included, keyed by node id.
func snapshotTasks(ctx context.Context, tx *sql.Tx, scope historyScope) (map[string]historySnapshot, error) {
	names := historyFieldNames()
	cols := ""
	for _, n := range names {
		cols += ", " + historyColumns[n]
	}
	// Read the scoped tasks (the task, plus its descendants on cascade) with
	// every recorded column; deleted rows are included so delete and undelete
	// are seen. Parameterized; the column list is a fixed registry.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(uid, '')`+cols+` FROM nodes
		 WHERE id = ? OR (? = 1 AND id LIKE ? ESCAPE '\')`,
		scope.id, boolInt(scope.cascade), escapeLIKEPrefix(scope.id)+".%")
	if err != nil {
		return nil, fmt.Errorf("snapshot history fields for %s: %w", scope.id, err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // read-only cursor, close error carries no data

	out := map[string]historySnapshot{}
	for rows.Next() {
		var id string
		snap := historySnapshot{values: map[string]sql.NullString{}}
		vals := make([]sql.NullString, len(names))
		dest := []any{&id, &snap.uid}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan history snapshot: %w", err)
		}
		for i, n := range names {
			snap.values[n] = vals[i]
		}
		if snap.uid == "" {
			snap.uid = id
		}
		out[id] = snap
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history snapshot: %w", err)
	}
	return out, nil
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
		id.String(), e.TaskUID, e.Timestamp.Format("2006-01-02T15:04:05Z"), e.Actor,
		string(e.Origin), string(e.Kind), e.Field, e.Old, e.New, e.EventID); err != nil {
		return fmt.Errorf("append history entry for %s: %w", e.TaskUID, err)
	}
	return nil
}

// historyFieldNames returns the recorded fields in a fixed order.
func historyFieldNames() []string {
	names := make([]string, 0, len(historyColumns))
	for n := range historyColumns {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
