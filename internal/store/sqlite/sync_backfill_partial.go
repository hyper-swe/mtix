// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// partialBackfillSet returns the live tasks whose creation the sync log lacks,
// when a backfill may send just those on a log that holds other events (round
// 4, ruling 2: an import merge into a store with tasks from before any sync
// leaves them unsent). It returns nil, meaning refuse, when no task lacks a
// creation or when the store ingests a relay (its log is not this store's to
// complete).
func (s *Store) partialBackfillSet(ctx context.Context) (map[string]bool, error) {
	var relay bool
	// A relay ingest cursor row: the log mixes peers' segments, so refuse.
	if err := s.readDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM relay_ingest_cursor)`).Scan(&relay); err != nil {
		return nil, fmt.Errorf("check relay cursor: %w", err)
	}
	if relay {
		return nil, nil
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT n.id FROM nodes n
		WHERE n.deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM sync_events e WHERE e.op_type = 'create_node'
			AND (e.node_id = n.id OR (n.uid <> '' AND ((e.uid = n.uid AND e.uid <> '') OR e.event_id = n.uid))))
		AND NOT EXISTS (SELECT 1 FROM applied_events a WHERE n.uid <> '' AND a.event_id = n.uid)`)
	if err != nil {
		return nil, fmt.Errorf("find tasks without a creation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	set := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		set[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find tasks without a creation: %w", err)
	}
	if len(set) == 0 {
		return nil, nil
	}
	return set, nil
}
