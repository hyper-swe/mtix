// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"time"
)

// LocalEvent is one journal row as the worktree enrolment lists it.
type LocalEvent struct {
	ID     string
	NodeID string
	Op     string
	Status string
	At     time.Time
}

// LocalEvents lists every sync_events row, oldest first, for the enrolment's
// local-only check (MTIX-112.17.16). Read-only.
func (s *Store) LocalEvents(ctx context.Context) ([]LocalEvent, error) {
	// Every journal row with its sync status; the caller decides which are local-only.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT event_id, node_id, op_type, sync_status, created_at FROM sync_events ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("list local events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LocalEvent
	for rows.Next() {
		var e LocalEvent
		var at string
		if err := rows.Scan(&e.ID, &e.NodeID, &e.Op, &e.Status, &at); err != nil {
			return nil, fmt.Errorf("scan local event: %w", err)
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local events: %w", err)
	}
	return out, nil
}
