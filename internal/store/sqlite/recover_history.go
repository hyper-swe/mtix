// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// salvageHistory bulk-reads task_history from the damaged database, best
// effort: a table or row it cannot read yields fewer entries, never an error
// (MTIX-95.31.19.5).
func salvageHistory(ctx context.Context, db *sql.DB, out *dbSalvage) {
	// Every history row; the caller decides whether the recovered file carries them.
	rows, err := db.QueryContext(ctx, `SELECT entry_id, task_uid, ts, actor, origin, kind, field,
		old_value, new_value, event_id FROM task_history`)
	if err != nil {
		out.historyNotes = append(out.historyNotes, fmt.Sprintf("task history unreadable: %v", err))
		return
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort salvage read; a close failure loses nothing already scanned
	skipped := 0
	for rows.Next() {
		var e exportHistoryEntry
		var oldV, newV, ev sql.NullString
		if scanErr := rows.Scan(&e.ID, &e.TaskUID, &e.TS, &e.Actor, &e.Origin, &e.Kind, &e.Field, &oldV, &newV, &ev); scanErr != nil {
			skipped++
			continue
		}
		if oldV.Valid {
			e.Old = &oldV.String
		}
		if newV.Valid {
			e.New = &newV.String
		}
		e.EventID = ev.String
		out.history = append(out.history, e)
	}
	if skipped > 0 {
		out.historyNotes = append(out.historyNotes, fmt.Sprintf("%d task history rows were unreadable and skipped", skipped))
	}
	if err := rows.Err(); err != nil {
		out.historyNotes = append(out.historyNotes, fmt.Sprintf("task history reading stopped early: %v", err))
	}
}

// dropMalformedHistory keeps the entries the standard import accepts
// (checkHistoryEntry, the validator export and import use) and reports the
// rest as one note with the count and the reasons, so a malformed row never
// makes the recovered file unimportable.
func dropMalformedHistory(entries []exportHistoryEntry, res *RecoverResult) []exportHistoryEntry {
	kept := entries[:0:0]
	var reasons []string
	dropped := 0
	for _, e := range entries {
		if err := checkHistoryEntry(e); err != nil {
			dropped++
			if len(reasons) < 5 {
				reasons = append(reasons, err.Error())
			}
			continue
		}
		kept = append(kept, e)
	}
	if dropped > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d task history entries were dropped as malformed: %s",
			dropped, strings.Join(reasons, "; ")))
	}
	return kept
}

// carryRecoveredHistory applies when the mirror is a format 2.1.0 file, that
// is, the board exported history: it gathers the history the database still
// holds, the mirror's per-node entries and its top-level list, drops
// repeated entry ids, puts each entry on the recovered node it belongs to and
// keeps the rest as the orphan list (MTIX-95.31.19.5). The recovered export
// is then labeled 2.1.0 (finishRecover).
func carryRecoveredHistory(nodes map[string]exportNode, db *dbSalvage, mirror *ExportData, res *RecoverResult) {
	if mirror == nil || mirror.SchemaVersion != SchemaVersionHistory {
		return
	}
	all := slices.Clone(db.history)
	for _, n := range mirror.Nodes {
		all = append(all, n.History...)
	}
	all = append(all, mirror.History...)
	// Validate before dropping repeated ids, so a malformed copy never hides
	// the intact copy of the same entry.
	all = dropMalformedHistory(all, res)
	seen := map[string]bool{}
	unique := all[:0:0]
	for _, e := range all {
		if !seen[e.ID] {
			seen[e.ID] = true
			unique = append(unique, e)
		}
	}
	res.Notes = append(res.Notes, db.historyNotes...)
	sortHistory(unique)
	ids := slices.Sorted(maps.Keys(nodes))
	list := make([]exportNode, 0, len(ids))
	for _, id := range ids {
		n := nodes[id]
		n.History = nil
		list = append(list, n)
	}
	db.orphans = attachHistory(list, unique)
	for _, n := range list {
		nodes[n.ID] = n
	}
	db.historyKept = len(unique)
	if len(unique) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%d task history entries were salvaged; the recovered file is export format %s",
			len(unique), SchemaVersionHistory))
	}
}
