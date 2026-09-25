// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
)

// Reads for the subtree rule of held task creations (MTIX-95.12, review r5
// S1/S2). Push decides whether an event belongs to a held creation's
// subtree by the task it is about, found by uid, not by the number the
// event names: a local renumber changes a task's number but not its uid,
// and another task can take the old number.

// PushSubject is the task a pending event is about: the event's uid and
// the current number of the node that has it (soft-deleted or not; empty
// when the event has no uid or no node has it, as after mtix gc purged it).
type PushSubject struct {
	UID, CurrentNodeID string
}

// PushSubjects returns the subject of each of eventIDs that has a
// sync_events row, keyed by event id, in one query (idx_nodes_uid).
func (s *Store) PushSubjects(ctx context.Context, eventIDs []string) (map[string]PushSubject, error) {
	out := make(map[string]PushSubject, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	ids, err := json.Marshal(eventIDs)
	if err != nil {
		return nil, fmt.Errorf("read push subjects: %w", err)
	}
	// The uid of each event in the bound JSON array, and the current number
	// of the node that has it.
	rows, err := s.Query(ctx, `
		SELECT e.event_id, COALESCE(e.uid, ''), COALESCE(n.id, '')
		FROM sync_events e
		LEFT JOIN nodes n ON n.uid = e.uid AND n.uid IS NOT NULL AND n.uid <> ''
		WHERE e.event_id IN (SELECT value FROM json_each(?))`, string(ids))
	if err != nil {
		return nil, fmt.Errorf("read push subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var subj PushSubject
		if err := rows.Scan(&id, &subj.UID, &subj.CurrentNodeID); err != nil {
			return nil, fmt.Errorf("read push subjects: %w", err)
		}
		out[id] = subj
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read push subjects: %w", err)
	}
	return out, nil
}

// DataVersion returns PRAGMA data_version read on the write connection
// (MTIX-95.12): it changes when another connection, in this process or
// another, commits to the database, and not for this store's own writes.
// Push reads it before each batch to learn whether tasks were renumbered
// under it.
func (s *Store) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	// The write connection's view of commits made by other connections.
	if err := s.WriteDB().QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read data version: %w", err)
	}
	return v, nil
}
