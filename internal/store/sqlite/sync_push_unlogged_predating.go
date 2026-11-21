// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// predatingDeletesQuery is the query PredatingDeletesNamedBy runs, with the
// bound hex-encoded JSON array of event ids. named is the uids on the lines
// of those events (their own task's, a link's target's, and those of the
// tasks above each). It lists the pending delete events of each such task
// that push does not hold and that arrived before the task's earliest
// logged creation (by uid or by event id), in arrival order.
func predatingDeletesQuery() string {
	return `
	WITH RECURSIVE
	hq(event_id, uid, node_id, op_type, payload) AS (
		SELECT event_id, COALESCE(uid, ''), node_id, op_type, payload FROM sync_events
		WHERE event_id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))),
	start(id, uid, parent_id) AS (
		SELECT n.id, n.uid, n.parent_id FROM hq h JOIN nodes n ON n.uid = h.uid AND n.uid <> '' WHERE h.uid <> ''
		UNION
		SELECT n.id, COALESCE(n.uid, ''), n.parent_id FROM hq h JOIN nodes n ON n.id = h.node_id WHERE h.uid = ''
		UNION
		SELECT n.id, COALESCE(n.uid, ''), n.parent_id FROM hq h
		JOIN nodes n ON n.id = json_extract(h.payload, '$.depends_on_node_id')
		WHERE +h.op_type IN ('link_dep', 'unlink_dep') AND json_valid(h.payload)),
	line(id, uid, parent_id, depth) AS (
		SELECT id, uid, parent_id, 0 FROM start
		UNION ALL
		SELECT p.id, COALESCE(p.uid, ''), p.parent_id, l.depth + 1 FROM line l JOIN nodes p ON p.id = l.parent_id
		WHERE l.depth < 64),
	named(uid) AS (
		SELECT uid FROM hq WHERE uid <> ''
		UNION
		SELECT uid FROM line WHERE uid <> '')
	SELECT d.event_id FROM named m CROSS JOIN sync_events d ON d.uid = m.uid AND d.uid <> ''
	WHERE +d.op_type = 'delete' AND d.sync_status = 'pending'
	  AND NOT EXISTS (SELECT 1 FROM sync_quarantine q WHERE q.event_id = d.event_id AND q.source = 'push')
	  AND d.rowid < (SELECT MIN(r) FROM (
	        SELECT c.rowid AS r FROM sync_events c WHERE c.uid = m.uid AND c.uid <> '' AND +c.op_type = 'create_node'
	        UNION ALL
	        SELECT c.rowid FROM sync_events c WHERE c.event_id = m.uid AND +c.op_type = 'create_node'))
	ORDER BY d.rowid`
}

// PredatingDeletesNamedBy returns the ids of the pending deletes push does
// not hold of every task on the lines of the held events heldIDs (their own
// task, a link's target, and the tasks above each) that arrived before the
// task's creation was logged (MTIX-112.17.16.37, lead ruling r2 2). Push
// reads them before it releases the holds heldIDs of unlogged tasks and
// holds them, retired: the released holds no longer name the task, and
// such a delete must never be sent, so a teammate's task live here and on
// the peers is not deleted on the peers.
func (s *Store) PredatingDeletesNamedBy(ctx context.Context, heldIDs []string) ([]string, error) {
	if len(heldIDs) == 0 {
		return nil, nil
	}
	ids, err := hexIDList(heldIDs)
	if err != nil {
		return nil, fmt.Errorf("read predating deletes: %w", err)
	}
	rows, err := s.Query(ctx, predatingDeletesQuery(), ids)
	if err != nil {
		return nil, fmt.Errorf("read predating deletes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read predating deletes: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read predating deletes: %w", err)
	}
	return out, nil
}
