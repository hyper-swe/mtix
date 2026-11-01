// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// workflowHistoryLimit bounds each side of each conflicting field's report.
const workflowHistoryLimit = 5

// addWorkflowHistory compares immutable entry identities, not wall clocks:
// shared entries are the available base and are omitted from both sides.
// Missing history proves nothing about who changed a field. Report planning
// reads inside the import transaction and never changes the history union.
func addWorkflowHistory(ctx context.Context, tx *sql.Tx, local, file *exportNode, diffs []WorkflowFieldDiff) error {
	entries, err := readWorkflowHistory(ctx, tx, taskHistoryKey(local))
	if err != nil {
		return err
	}
	shared := sharedWorkflowHistory(entries, file.History)
	for i := range diffs {
		d := &diffs[i]
		d.LocalHistory, d.LocalHistoryMore = workflowHistorySide(entries, shared, d.Field)
		d.FileHistory, d.FileHistoryMore = workflowHistorySide(file.History, shared, d.Field)
	}
	return nil
}

// readWorkflowHistory reads the task's stored history independent of the
// export switch; history.export controls sharing, not local visibility.
func readWorkflowHistory(ctx context.Context, tx *sql.Tx, uid string) (out []exportHistoryEntry, err error) {
	// Stable task identity isolates the report from other tasks and renumbers.
	rows, err := tx.QueryContext(ctx, `SELECT entry_id, task_uid, ts, actor, origin, kind, field,
  old_value, new_value, event_id FROM task_history WHERE task_uid = ?`, uid)
	if err != nil {
		return nil, fmt.Errorf("read workflow history: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close workflow history: %w", closeErr)
		}
	}()
	for rows.Next() {
		var e exportHistoryEntry
		var oldV, newV, event sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskUID, &e.TS, &e.Actor, &e.Origin, &e.Kind, &e.Field, &oldV, &newV, &event); err != nil {
			return nil, fmt.Errorf("scan workflow history: %w", err)
		}
		if oldV.Valid {
			e.Old = &oldV.String
		}
		if newV.Valid {
			e.New = &newV.String
		}
		e.EventID = event.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflow history: %w", err)
	}
	return out, nil
}

// sharedWorkflowHistory recognizes only identical records as common history.
// A reused identity with different content is left visible on both sides and
// remains subject to the normal import union's conflict diagnostics.
func sharedWorkflowHistory(local, file []exportHistoryEntry) map[string]bool {
	byID := make(map[string]exportHistoryEntry, len(local))
	for _, e := range local {
		byID[e.ID] = e
	}
	shared := map[string]bool{}
	for _, e := range file {
		if l, ok := byID[e.ID]; ok && sameHistoryEntry(l, e) {
			shared[e.ID] = true
		}
	}
	return shared
}

// workflowHistorySide returns the newest entries absent from the shared base,
// with deterministic entry-id order for equal timestamps and a remaining count.
func workflowHistorySide(entries []exportHistoryEntry, shared map[string]bool, field string) ([]exportHistoryEntry, int) {
	out := []exportHistoryEntry{}
	for _, e := range entries {
		if e.Field == field && !shared[e.ID] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS > out[j].TS
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > workflowHistoryLimit {
		return out[:workflowHistoryLimit], len(out) - workflowHistoryLimit
	}
	return out, 0
}

// appendWorkflowResolution records the explicit choice even when ours keeps
// every value. The field identifies the side and affected fields, never values.
// It shares the import transaction and clock; a failure rolls everything back.
func appendWorkflowResolution(ctx context.Context, tx *sql.Tx, n *exportNode, c *WorkflowConflict) error {
	var uid string
	// Resolve after the merge: a legacy file can omit the durable uid kept locally.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(uid, ''), id) FROM nodes WHERE id = ?`, n.ID).Scan(&uid); err != nil {
		return fmt.Errorf("resolve workflow history identity: %w", err)
	}
	fields := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		fields = append(fields, f.Field)
	}
	return appendHistoryEntry(ctx, tx, model.HistoryEntry{
		TaskUID: uid, Timestamp: c.at.UTC(), Actor: resolveEmitAuthor(ctx, tx, ""),
		Origin: model.HistoryOriginImport, Kind: model.HistoryKindResolution,
		Field: "resolution:" + string(c.Choice) + ":" + strings.Join(fields, ","),
	})
}

// writeWorkflowHistory quotes every stored string at the terminal boundary;
// JSON retains the original structured entries and explicit truncation counts.
func writeWorkflowHistory(b *strings.Builder, side string, entries []exportHistoryEntry, more int) {
	fmt.Fprintf(b, "        %s history (entries outside shared history):\n", side)
	if len(entries) == 0 {
		b.WriteString("          no entries supplied outside shared history; not proof of no changes\n")
	}
	for _, e := range entries {
		fmt.Fprintf(b, "          %q %q by %q (%q, %q)", e.TS, e.ID, e.Actor, e.Origin, e.Kind)
		if e.Old != nil {
			fmt.Fprintf(b, " old=%q", *e.Old)
		}
		if e.New != nil {
			fmt.Fprintf(b, " new=%q", *e.New)
		}
		b.WriteString("\n")
	}
	if more > 0 {
		fmt.Fprintf(b, "          %d more\n", more)
	}
}
