// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// An automatic import replaces the board with the file a teammate delivered,
// and journals nothing: the file is the teammate's copy of the same board,
// so journaling it would push their changes again under new event ids
// (MTIX-112.17.16.16). A value it changes on a task the hub already knows
// therefore stays local, and a later pull of a hub edit may replace it. These
// functions record such tasks, with the fields that changed, so the notice
// and mtix doctor can name them.

// localOnlyKey is the meta key holding the recorded local-only imports.
const localOnlyKey = "sync.local_only_imports"

// maxLocalOnlyRecorded is the default bound on the tasks kept; the rest are
// counted.
const maxLocalOnlyRecorded = 20000

// localOnlyEntry is the stored form of one task: the newest sync log row when
// its change was recorded (an event row after it can settle a field, a
// position that needs no clock) and the fields that changed.
type localOnlyEntry struct {
	After  int64    `json:"after"`
	Fields []string `json:"fields,omitempty"`
}

// localOnlyRecord is the stored form: the tasks last changed by an import
// that did not journal, and how many tasks did not fit.
type localOnlyRecord struct {
	Tasks    map[string]localOnlyEntry `json:"tasks"`
	Overflow int                       `json:"overflow,omitempty"`
}

// LocalOnlyImport is a task an automatic import changed that the hub already
// knows, with the fields whose change is still local. Fields no sync event
// carries are not recorded: nothing replaces or sends them.
type LocalOnlyImport struct {
	ID     string   `json:"id"`
	Fields []string `json:"fields,omitempty"`
}

// HubKnownNodes returns those of ids that the sync log holds an event for,
// pushed, pulled or still pending: the log carries the task, so a value an
// import changes without journaling it never reaches the hub (an unpushed
// create carries its old payload, and a relay publisher's events stay
// pending). Callers ask only for a store that syncs (MTIX-112.17.16.16).
func (s *Store) HubKnownNodes(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	nodes, err := loggedNodes(ctx, s.readDB)
	if err != nil {
		return nil, fmt.Errorf("read the tasks the sync log holds: %w", err)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var known []string
	for _, n := range nodes {
		if want[n.id] {
			known = append(known, n.id)
		}
	}
	sort.Strings(known)
	return known, nil
}

func (s *Store) localOnlyLimitOrDefault() int {
	if s.localOnlyLimit > 0 {
		return s.localOnlyLimit
	}
	return maxLocalOnlyRecorded
}

// RecordLocalOnlyImports notes that an import changed these tasks, in these
// fields, without journaling the change (MTIX-112.17.16.16). A task already
// recorded keeps one entry: the fields join and the position moves up. Tasks
// whose fields have all settled are pruned first, so the record has room
// again; a task that still does not fit is counted.
func (s *Store) RecordLocalOnlyImports(ctx context.Context, changes []LocalOnlyImport) error {
	if len(changes) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := readLocalOnly(ctx, tx)
		if err != nil {
			return err
		}
		if pruneErr := pruneSettled(ctx, tx, &rec); pruneErr != nil {
			return pruneErr
		}
		var newest int64
		// The newest sync log row now: an event row after it settles a recorded field.
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0) FROM sync_events`).Scan(&newest); err != nil {
			return fmt.Errorf("read the newest sync log row: %w", err)
		}
		limit := s.localOnlyLimitOrDefault()
		for _, c := range changes {
			old, held := rec.Tasks[c.ID]
			if !held && len(rec.Tasks) >= limit {
				rec.Overflow++
				continue
			}
			rec.Tasks[c.ID] = localOnlyEntry{After: newest, Fields: mergeFields(old.Fields, c.Fields)}
		}
		return writeLocalOnly(ctx, tx, rec)
	})
}

func mergeFields(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range append(append([]string(nil), a...), b...) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func writeLocalOnly(ctx context.Context, tx *sql.Tx, rec localOnlyRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode the local-only imports: %w", err)
	}
	// meta holds the record as one JSON value under a fixed key.
	if _, err = tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, localOnlyKey, string(raw)); err != nil {
		return fmt.Errorf("store the local-only imports: %w", err)
	}
	return nil
}

func readLocalOnly(ctx context.Context, q rowQuerier) (localOnlyRecord, error) {
	rec := localOnlyRecord{Tasks: map[string]localOnlyEntry{}}
	var raw string
	// The recorded local-only imports, if any.
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, localOnlyKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("read the local-only imports: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return rec, fmt.Errorf("decode the local-only imports: %w", err)
	}
	if rec.Tasks == nil {
		rec.Tasks = map[string]localOnlyEntry{}
	}
	return rec, nil
}

// liveFields returns the fields of a recorded task that no later event has
// settled, and whether the task is settled altogether: an event for a field
// after the recorded position, local or pulled, settles that field. An entry
// without fields (recorded as a bare id) settles on any later event.
func liveFields(ctx context.Context, q rowQuerier, id string, e localOnlyEntry) (live []string, settled bool, err error) {
	rows, err := q.QueryContext(ctx, `SELECT op_type, payload FROM sync_events
		WHERE rowid > ? AND (node_id = ? OR (uid IS NOT NULL AND uid <> '' AND uid = (SELECT uid FROM nodes WHERE id = ?)))`,
		e.After, id, id)
	if err != nil {
		return nil, false, fmt.Errorf("read later events of %s: %w", id, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close later events of %s: %w", id, closeErr)
		}
	}()
	done := map[string]bool{}
	later := false
	for rows.Next() {
		var op, payload string
		if scanErr := rows.Scan(&op, &payload); scanErr != nil {
			return nil, false, fmt.Errorf("read later events of %s: %w", id, scanErr)
		}
		later = true
		for _, f := range fieldsOfEvent(op, payload) {
			done[f] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read later events of %s: %w", id, err)
	}
	if len(e.Fields) == 0 {
		return nil, later, nil
	}
	for _, f := range e.Fields {
		if !done[f] {
			live = append(live, f)
		}
	}
	return live, len(live) == 0, nil
}

// pruneSettled drops the tasks of rec that have settled, and the overflow
// count with them once no task is left.
func pruneSettled(ctx context.Context, q rowQuerier, rec *localOnlyRecord) error {
	for id, e := range rec.Tasks {
		_, settled, err := liveFields(ctx, q, id, e)
		if err != nil {
			return err
		}
		if settled {
			delete(rec.Tasks, id)
		}
	}
	if len(rec.Tasks) == 0 {
		rec.Overflow = 0
	}
	return nil
}

// LocalOnlyImports lists the recorded tasks with their fields still local,
// and the number of tasks that did not fit the record. A field drops out once
// the sync log holds a later event for it: a local edit journals the current
// value, and a pulled event replaced it (MTIX-112.17.16.16). A task whose
// fields have all settled drops out; with no task left the count clears.
func (s *Store) LocalOnlyImports(ctx context.Context) (live []LocalOnlyImport, overflow int, err error) {
	rec, err := readLocalOnly(ctx, s.readDB)
	if err != nil {
		return nil, 0, err
	}
	for id, e := range rec.Tasks {
		fields, settled, err := liveFields(ctx, s.readDB, id, e)
		if err != nil {
			return nil, 0, err
		}
		if !settled {
			live = append(live, LocalOnlyImport{ID: id, Fields: fields})
		}
	}
	sort.Slice(live, func(a, b int) bool { return live[a].ID < live[b].ID })
	if len(live) == 0 {
		return live, 0, nil
	}
	return live, rec.Overflow, nil
}

// SetLocalOnlyRecordLimit sets how many tasks the local-only record keeps
// (default maxLocalOnlyRecorded); tasks beyond it are counted. Tests set a
// small limit.
func (s *Store) SetLocalOnlyRecordLimit(n int) { s.localOnlyLimit = n }
