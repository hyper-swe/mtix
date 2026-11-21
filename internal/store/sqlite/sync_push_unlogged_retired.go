// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// UnloggedHoldPrefix starts the reason of a push hold of an event that
// depends on a task the sync log has no creation for (MTIX-112.17.16.37).
const UnloggedHoldPrefix = "no logged creation: "

// Retired holds of unlogged tasks (MTIX-112.17.16.37, reviews r1b and r1c,
// lead rulings). A task whose events push held as unlogged and that this
// replica deleted BEFORE the task's creation reached the log (doctor's
// re-create fix, or mtix gc) keeps that delete local for good, and, while
// it stays deleted or purged, every other event about it: a later pull of
// a teammate's creation of the same task does not release them, so the
// teammate's live task is never deleted on the peers by a local cleanup.
// Order is the local log's arrival order (sync_events rowid). A delete made
// after the creation is logged is an ordinary delete of a shared task and is
// sent after the held edits. A task live here (undeleted) releases its held
// edits once its creation is logged; only its predating delete stays. An
// event whose own task has no node and no logged creation (gc purged an
// unlogged task before its delete was pushed) is retired too.

// retiredUnloggedQuery is the query readRetiredUnlogged runs, with the
// bound hex-encoded JSON array of event ids, then UnloggedHoldPrefix twice.
// For each event and the task it names (its own, or a link's target, by
// uid) that has an unlogged push hold of an event of its own (not a link or
// unlink, whose task may be logged) and a logged creation, it lists the
// event when it is that task's delete and arrived before the creation, or
// when the task's latest delete arrived before the creation and the task is
// not live here. It also lists each event whose own task has no node and
// no logged creation. Lookups go through idx_sync_events_uid, the primary
// keys and idx_nodes_uid; the unary plus on op_type keeps the planner off
// idx_sync_events_op_type.
func retiredUnloggedQuery() string {
	return `
	WITH
	batch(event_id, rid, uid, node_id, op_type, payload) AS (
		SELECT event_id, rowid, COALESCE(uid, ''), node_id, op_type, payload FROM sync_events
		WHERE event_id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))),
	subj(event_id, rid, own_op, node_id, uid) AS (
		SELECT event_id, rid, op_type, node_id, uid FROM batch WHERE uid <> ''
		UNION
		SELECT b.event_id, b.rid, 'target', n.id, n.uid FROM batch b
		JOIN nodes n ON n.id = json_extract(b.payload, '$.depends_on_node_id')
		WHERE +b.op_type IN ('link_dep', 'unlink_dep') AND json_valid(b.payload) AND COALESCE(n.uid, '') <> ''),
	held(event_id, rid, own_op, node_id, uid, cu, ce, last_del) AS (
		SELECT s.event_id, s.rid, s.own_op, s.node_id, s.uid,
		       (SELECT MIN(c.rowid) FROM sync_events c WHERE c.uid = s.uid AND c.uid <> '' AND +c.op_type = 'create_node'),
		       (SELECT MIN(c.rowid) FROM sync_events c WHERE c.event_id = s.uid AND +c.op_type = 'create_node'),
		       (SELECT MAX(d.rowid) FROM sync_events d WHERE d.uid = s.uid AND d.uid <> '' AND +d.op_type = 'delete')
		FROM subj s
		WHERE EXISTS (SELECT 1 FROM sync_events h JOIN sync_quarantine q ON q.event_id = h.event_id
		              WHERE h.uid = s.uid AND h.uid <> '' AND +h.op_type NOT IN ('link_dep', 'unlink_dep')
		                AND q.source = 'push' AND substr(q.reason, 1, length(?)) = ?)),
	logged(event_id, rid, own_op, node_id, uid, cr, last_del) AS (
		SELECT event_id, rid, own_op, node_id, uid, COALESCE(MIN(cu, ce), cu, ce), last_del FROM held)
	SELECT l.event_id, l.node_id, l.uid FROM logged l
	WHERE l.cr IS NOT NULL
	  AND ((l.own_op = 'delete' AND l.rid < l.cr)
	       OR (l.last_del < l.cr
	           AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid = l.uid AND n.uid <> '' AND n.deleted_at IS NULL)))
	UNION
	SELECT b.event_id, b.node_id, b.uid FROM batch b
	WHERE b.uid <> ''
	  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid = b.uid AND n.uid <> '')
	  AND NOT EXISTS (SELECT 1 FROM sync_events c WHERE c.uid = b.uid AND c.uid <> '' AND +c.op_type = 'create_node')
	  AND NOT EXISTS (SELECT 1 FROM sync_events c WHERE c.event_id = b.uid AND +c.op_type = 'create_node')`
}

// readRetiredUnlogged returns, keyed by event id, the retired subject of
// each event of the bound id list that retiredUnloggedQuery lists.
func readRetiredUnlogged(ctx context.Context, q pushQuerier, ids string) (map[string]UnloggedSubject, error) {
	rows, err := q.QueryContext(ctx, retiredUnloggedQuery(), ids, UnloggedHoldPrefix, UnloggedHoldPrefix)
	if err != nil {
		return nil, fmt.Errorf("query retired unlogged holds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]UnloggedSubject{}
	for rows.Next() {
		var eventID string
		var s UnloggedSubject
		if err := rows.Scan(&eventID, &s.NodeID, &s.UID); err != nil {
			return nil, fmt.Errorf("scan retired unlogged hold: %w", err)
		}
		if _, seen := out[eventID]; !seen {
			s.Retired = true
			out[eventID] = s
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read retired unlogged holds: %w", err)
	}
	return out, nil
}
