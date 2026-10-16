// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// SchemaVersionHistory is the schema_version of an export that carries task
// history (MTIX-95.31.19.5, ADR-006 A3.1). It is a minor bump of
// SchemaVersionV1: a reader of this build accepts it (FR-15.2g compares the
// major version only). An export with the history.export switch off is still
// written as SchemaVersionV1, byte for byte as before.
const SchemaVersionHistory = "2.1.0"

// exportHistoryEntry is one task_history row in an export. ts is the stored
// fixed-width UTC text, kept verbatim so a round trip is exact; old and new
// are JSON text for a structured field and absent for free text and for a
// SQL NULL.
type exportHistoryEntry struct {
	ID      string  `json:"id"`
	TaskUID string  `json:"task_uid"`
	TS      string  `json:"ts"`
	Actor   string  `json:"actor"`
	Origin  string  `json:"origin"`
	Kind    string  `json:"kind"`
	Field   string  `json:"field"`
	Old     *string `json:"old,omitempty"`
	New     *string `json:"new,omitempty"`
	EventID string  `json:"event_id,omitempty"`
}

// SetHistoryExportSource installs the reader of the history.export switch
// (config key, default off). Nil, the default, means off. Call once while
// wiring the store.
func (s *Store) SetHistoryExportSource(on func() bool) { s.historyExport = on }

// SetWriterVersion sets the mtix version an export carrying history records
// as its last writer when the caller names none.
func (s *Store) SetWriterVersion(v string) { s.writerVersion = v }

func (s *Store) historyExportOn() bool { return s.historyExport != nil && s.historyExport() }

// taskHistoryKey is the identity task_history rows use for a node: its uid,
// or its id when it has none (task_history_schema.go).
func taskHistoryKey(n *exportNode) string {
	if n.UID != "" {
		return n.UID
	}
	return n.ID
}

// readExportHistory reads every task_history row through q, oldest first per
// task. It returns nil when history.export is off, and a non-nil (possibly
// empty) slice when it is on (MTIX-95.31.19.5).
func (s *Store) readExportHistory(ctx context.Context, q queryable) ([]exportHistoryEntry, error) {
	if !s.historyExportOn() {
		return nil, nil
	}
	// Every history row, in the order the export writes it: by task, then time.
	rows, err := q.QueryContext(ctx, `SELECT entry_id, task_uid, ts, actor, origin, kind, field,
		old_value, new_value, event_id FROM task_history ORDER BY task_uid, ts, entry_id`)
	if err != nil {
		return nil, fmt.Errorf("query task history: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close export history rows", "error", closeErr)
		}
	}()
	out := []exportHistoryEntry{}
	for rows.Next() {
		var e exportHistoryEntry
		var oldV, newV, ev sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskUID, &e.TS, &e.Actor, &e.Origin, &e.Kind, &e.Field, &oldV, &newV, &ev); err != nil {
			return nil, fmt.Errorf("scan task history: %w", err)
		}
		if oldV.Valid {
			e.Old = &oldV.String
		}
		if newV.Valid {
			e.New = &newV.String
		}
		e.EventID = ev.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// attachHistory puts each entry on the node whose history key it carries and
// returns the entries of no node (orphans), in the checksum order. A nil
// history attaches nothing and returns nil.
func attachHistory(nodes []exportNode, history []exportHistoryEntry) []exportHistoryEntry {
	if len(history) == 0 {
		return nil
	}
	byKey := make(map[string]int, len(nodes))
	for i := range nodes {
		byKey[taskHistoryKey(&nodes[i])] = i
	}
	var orphans []exportHistoryEntry
	for _, e := range history {
		if i, ok := byKey[e.TaskUID]; ok {
			nodes[i].History = append(nodes[i].History, e)
			continue
		}
		orphans = append(orphans, e)
	}
	sortHistory(orphans)
	return orphans
}

// sortHistory orders entries by task, then time, then entry id: the order the
// top-level list is written and hashed in.
func sortHistory(h []exportHistoryEntry) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].TaskUID != h[j].TaskUID {
			return h[i].TaskUID < h[j].TaskUID
		}
		if h[i].TS != h[j].TS {
			return h[i].TS < h[j].TS
		}
		return h[i].ID < h[j].ID
	})
}

// validateExportHistory refuses a file whose history entries cannot be stored
// (a missing identity, actor or field, a kind, origin or time outside the
// forms mtix writes), a node entry that names another task, and a top-level
// (orphan) entry that names a task the file holds: a task's history lives on
// its node, and an orphan entry must not move onto a live task. It runs
// before anything is written.
func validateExportHistory(data *ExportData) error {
	live := make(map[string]bool, len(data.Nodes))
	for i := range data.Nodes {
		live[taskHistoryKey(&data.Nodes[i])] = true
	}
	for i := range data.Nodes {
		n := &data.Nodes[i]
		for _, e := range n.History {
			if e.TaskUID != taskHistoryKey(n) {
				return fmt.Errorf("history entry %q on task %s names another task: %w", e.ID, n.ID, model.ErrInvalidInput)
			}
			if err := checkHistoryEntry(e); err != nil {
				return err
			}
		}
	}
	for _, e := range data.History {
		if live[e.TaskUID] {
			return fmt.Errorf("history entry %q in the top-level list names task %s, which the file holds: %w",
				e.ID, e.TaskUID, model.ErrInvalidInput)
		}
		if err := checkHistoryEntry(e); err != nil {
			return err
		}
	}
	return nil
}

// checkHistoryEntry validates one entry's own fields: all present, kind and
// origin among the values mtix writes, ts in the fixed-width UTC form that
// task_history stores (it sorts as text).
func checkHistoryEntry(e exportHistoryEntry) error {
	for name, v := range map[string]string{"id": e.ID, "task_uid": e.TaskUID, "ts": e.TS,
		"actor": e.Actor, "origin": e.Origin, "kind": e.Kind, "field": e.Field} {
		if v == "" {
			return fmt.Errorf("history entry %q has no %s: %w", e.ID, name, model.ErrInvalidInput)
		}
	}
	switch model.HistoryKind(e.Kind) {
	case model.HistoryKindChange, model.HistoryKindRedaction, model.HistoryKindRetentionDrop:
	default:
		return fmt.Errorf("history entry %q has unknown kind %q: %w", e.ID, e.Kind, model.ErrInvalidInput)
	}
	switch model.HistoryOrigin(e.Origin) {
	case model.HistoryOriginLocal, model.HistoryOriginImport, model.HistoryOriginHub, model.HistoryOriginSystem:
	default:
		return fmt.Errorf("history entry %q has unknown origin %q: %w", e.ID, e.Origin, model.ErrInvalidInput)
	}
	if t, err := time.Parse(historyTimeLayout, e.TS); err != nil || t.Format(historyTimeLayout) != e.TS {
		return fmt.Errorf("history entry %q has ts %q, not %s: %w", e.ID, e.TS, historyTimeLayout, model.ErrInvalidInput)
	}
	return nil
}

// insertExportHistory unions the history a file carries, per node and in the
// top-level list, into the store by entry id, and records the outcome on
// result (MTIX-95.31.19.9, ADR-006 A3). Replace and merge imports share it: a
// local entry is never dropped or changed, an entry the store holds
// identically is counted as skipped, one it holds with other content keeps
// the local row and counts as a conflict, and one keyed by a legacy dot id
// (not a task uid) is skipped and counted. The union is idempotent and does
// not depend on the order of the entries; an entry id the file itself repeats
// with different content is skipped whole (see uniqueEntries).
func insertExportHistory(ctx context.Context, tx *sql.Tx, data *ExportData, result *ImportResult, now time.Time) error {
	u := historyUnion{}
	if !now.IsZero() { // a caller with no clock applies no skew limit
		u.now = now.UTC().Format(historyTimeLayout)
		u.latest = now.UTC().Add(historyFutureSkew).Format(historyTimeLayout)
	}
	if err := u.loadCutoffs(ctx, tx); err != nil {
		return err
	}
	for _, e := range u.uniqueEntries(data) {
		if err := u.add(ctx, tx, e); err != nil {
			return err
		}
	}
	u.report(result)
	return nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
