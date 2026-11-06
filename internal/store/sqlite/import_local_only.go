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
// functions record such tasks, so the notice and mtix doctor can name them.

// localOnlyKey is the meta key holding the recorded local-only imports.
const localOnlyKey = "sync.local_only_imports"

// maxLocalOnlyRecorded bounds the ids kept; the rest are only counted.
const maxLocalOnlyRecorded = 1000

// localOnlyRecord is the stored form: for each task last changed by an import
// that did not journal, the newest sync log row at that moment (an event row
// after it settles the task, a position that needs no clock), and how many
// ids did not fit.
type localOnlyRecord struct {
	After    map[string]int64 `json:"after"`
	Overflow int              `json:"overflow,omitempty"`
}

// LocalOnlyImport is a task an automatic import changed that the hub already
// knows, with the change still local.
type LocalOnlyImport struct {
	ID string `json:"id"`
}

// HubKnownNodes returns those of ids whose creation is past pending in the
// sync log: the hub knows the task, by a push or by a pull (MTIX-112.17.16.16).
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
		if want[n.id] && n.pushed {
			known = append(known, n.id)
		}
	}
	sort.Strings(known)
	return known, nil
}

// RecordLocalOnlyImports notes that an import changed these tasks without
// journaling the change (MTIX-112.17.16.16). A task already recorded keeps
// one entry with the newer time.
func (s *Store) RecordLocalOnlyImports(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		rec, err := readLocalOnly(ctx, tx)
		if err != nil {
			return err
		}
		var newest int64
		// The newest sync log row now: an event row after it settles a recorded task.
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0) FROM sync_events`).Scan(&newest); err != nil {
			return fmt.Errorf("read the newest sync log row: %w", err)
		}
		for _, id := range ids {
			if _, held := rec.After[id]; !held && len(rec.After) >= maxLocalOnlyRecorded {
				rec.Overflow++
				continue
			}
			rec.After[id] = newest
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("encode the local-only imports: %w", err)
		}
		// meta holds the record as one JSON value under a fixed key.
		_, err = tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, localOnlyKey, string(raw))
		return err
	})
}

func readLocalOnly(ctx context.Context, q rowQuerier) (localOnlyRecord, error) {
	rec := localOnlyRecord{After: map[string]int64{}}
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
	if rec.After == nil {
		rec.After = map[string]int64{}
	}
	return rec, nil
}

// LocalOnlyImports lists the recorded tasks whose imported change is still
// local, with the number of ids that did not fit the record. A task drops out
// once the sync log holds a later event row for it: a local edit journals the
// current value, and a pulled event replaced it (MTIX-112.17.16.16).
func (s *Store) LocalOnlyImports(ctx context.Context) (live []LocalOnlyImport, overflow int, err error) {
	rec, err := readLocalOnly(ctx, s.readDB)
	if err != nil {
		return nil, 0, err
	}
	for id, after := range rec.After {
		var later bool
		// A later event for the task, local or pulled, settles it.
		if err := s.readDB.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM sync_events WHERE node_id = ? AND rowid > ?)`, id, after).Scan(&later); err != nil {
			return nil, 0, fmt.Errorf("check later events of %s: %w", id, err)
		}
		if !later {
			live = append(live, LocalOnlyImport{ID: id})
		}
	}
	sort.Slice(live, func(a, b int) bool { return live[a].ID < live[b].ID })
	return live, rec.Overflow, nil
}
