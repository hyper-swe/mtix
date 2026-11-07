// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"sort"
)

// partialBackfillSet returns the live tasks whose creation the sync log lacks,
// when a backfill may send just those on a log that holds other events (round
// 4, ruling 2: an import merge into a store with tasks from before any sync
// leaves them unsent). It returns nil, meaning refuse, when no task lacks a
// creation or when the store ingests a relay (its log is not this store's to
// complete).
func (s *Store) partialBackfillSet(ctx context.Context) (set map[string]bool, notSent []string, err error) {
	var relay bool
	// A relay ingest cursor row: the log mixes peers' segments, so refuse.
	err = s.readDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM relay_ingest_cursor)`).Scan(&relay)
	if err != nil {
		return nil, nil, fmt.Errorf("check relay cursor: %w", err)
	}
	if relay {
		return nil, nil, nil
	}
	parents, err := s.tasksWithoutCreation(ctx)
	if err != nil {
		return nil, nil, err
	}
	set, notSent, err = s.dropOrphans(ctx, parents)
	if len(set) == 0 {
		return nil, notSent, err
	}
	return set, notSent, err
}

// tasksWithoutCreation maps each live task the sync log holds no creation for
// to its parent id.
func (s *Store) tasksWithoutCreation(ctx context.Context) (map[string]string, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT n.id, COALESCE(n.parent_id, '') FROM nodes n
		WHERE n.deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM sync_events e WHERE e.op_type = 'create_node'
			AND (e.node_id = n.id OR (n.uid <> '' AND ((e.uid = n.uid AND e.uid <> '') OR e.event_id = n.uid))))
		AND NOT EXISTS (SELECT 1 FROM applied_events a WHERE n.uid <> '' AND a.event_id = n.uid)`)
	if err != nil {
		return nil, fmt.Errorf("find tasks without a creation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	parents := map[string]string{}
	for rows.Next() {
		var id, parent string
		if scanErr := rows.Scan(&id, &parent); scanErr != nil {
			return nil, fmt.Errorf("scan task: %w", scanErr)
		}
		parents[id] = parent
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("find tasks without a creation: %w", err)
	}
	return parents, nil
}

// logHoldsCreation reports whether the sync log (or an applied hub event)
// holds the creation of task id.
func (s *Store) logHoldsCreation(ctx context.Context, id string) (bool, error) {
	var held bool
	err := s.readDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sync_events e WHERE e.op_type = 'create_node' AND e.node_id = ?)
		OR EXISTS (SELECT 1 FROM nodes p WHERE p.id = ? AND p.uid <> '' AND (EXISTS (SELECT 1 FROM sync_events e
			WHERE e.op_type = 'create_node' AND (e.uid = p.uid OR e.event_id = p.uid))
		OR EXISTS (SELECT 1 FROM applied_events a WHERE a.event_id = p.uid)))`, id, id).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("check the creation of %s: %w", id, err)
	}
	return held, nil
}

// dropOrphans splits the tasks the log lacks into those a backfill can send
// and those it cannot: a task under a parent that is neither sent with it nor
// created in the log (a deleted parent) has nothing for a peer to attach to,
// and the same goes for everything under it.
func (s *Store) dropOrphans(ctx context.Context, parents map[string]string) (map[string]bool, []string, error) {
	ok := map[string]bool{}
	var visit func(id string) (bool, error)
	visit = func(id string) (bool, error) {
		if v, seen := ok[id]; seen {
			return v, nil
		}
		parent := parents[id]
		var good bool
		var err error
		switch _, sent := parents[parent]; {
		case parent == "":
			good = true
		case sent:
			good, err = visit(parent)
		default: // the parent is not among the tasks to send: fine only if the log holds its creation
			good, err = s.logHoldsCreation(ctx, parent)
		}
		if err != nil {
			return false, err
		}
		ok[id] = good
		return good, nil
	}
	set := map[string]bool{}
	var notSent []string
	for id := range parents {
		good, err := visit(id)
		if err != nil {
			return nil, nil, err
		}
		if good {
			set[id] = true
		} else {
			notSent = append(notSent, id)
		}
	}
	sort.Strings(notSent)
	return set, notSent, nil
}
