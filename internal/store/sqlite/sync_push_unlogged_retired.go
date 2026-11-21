// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
)

// UnloggedHoldPrefix starts the reason of a push hold of an event that
// depends on a task the sync log has no creation for (MTIX-112.17.16.37).
const UnloggedHoldPrefix = "no logged creation: "

// UnloggedPurgedPrefix starts the reason of such a hold whose task this
// replica purged (mtix gc) before the sync log held its creation (lead
// ruling r2 3): the hold is retired for good, even when a later pull brings
// the task back here live. It starts with UnloggedHoldPrefix.
const UnloggedPurgedPrefix = UnloggedHoldPrefix + "purged: "

// UnloggedPurgedReason is the reason of a hold of an event of task nodeID
// (uid uid) that was purged here before the sync log held its creation. The
// task number is folded to one line without control characters and the uid
// is rendered with format.EventID.
func UnloggedPurgedReason(nodeID, uid string) string {
	return fmt.Sprintf("%stask %s (uid %s) was purged from this board before the sync log held its creation, "+
		"so this change is never sent; see mtix sync doctor", UnloggedPurgedPrefix,
		strings.Join(strings.Fields(format.StripControlChars(nodeID)), " "), format.EventID(uid))
}

// Retired holds of unlogged tasks (MTIX-112.17.16.37, reviews r1b to r1d,
// lead rulings r1d and r2). A hold names a task: the topmost task its event
// depends on that the log had no creation for, and with it every task on
// the event's lines (its own task, a link's target, and the tasks above
// each). A delete of a task named by an unlogged push hold that arrived
// BEFORE the task's creation reached the log (doctor's re-create fix) stays
// local for good, whichever held event named the task: its own, a held child
// creation, a held event of a task below it or a held link to or from it.
// While such a task stays deleted, every event on its lines stays local too,
// so a later pull of a teammate's creation of the same task never deletes
// the teammate's live task on the peers. Order is the local log's arrival
// order (sync_events rowid), and the task's LATEST delete decides: a delete
// made after the creation is logged is an ordinary delete of a shared task
// and is sent after the held edits. A task live here (undeleted) releases
// its held events once its creation is logged; only its predating delete
// stays. A gc purge counts as a delete that predates any later logged
// creation: gc marks the held events of the purged task (PurgeDeletedNode),
// an event whose own task has no node and no logged creation is retired as
// purged, and both stay retired after a pull brings the task back.

// retiredUnloggedQuery is the query readRetiredUnlogged runs, with the
// bound hex-encoded JSON array of event ids, then UnloggedHoldPrefix twice
// and UnloggedPurgedPrefix twice. named is the uids every unlogged push hold names:
// its own task's and those on its lines. For each event of the batch and
// each task on its lines (subj, own_delete set for the event's own task
// when the event is a delete) that a hold
// names and that has a logged creation (the earliest, by uid or by event
// id), it lists the event when it is that task's own delete and arrived
// before the creation, or when the task's latest delete arrived before the
// creation and the task is not live here. It also lists, as purged, each
// event whose own hold is marked purged and each event whose own task has no
// node and no logged creation. Lookups go through idx_sync_events_uid, the
// primary keys and idx_nodes_uid; only sync_quarantine is scanned (the push
// holds). The unary plus on op_type keeps the planner off
// idx_sync_events_op_type.
func retiredUnloggedQuery() string {
	return `
	WITH RECURSIVE
	batch(event_id, rid, uid, node_id, op_type, payload) AS (
		SELECT event_id, rowid, COALESCE(uid, ''), node_id, op_type, payload FROM sync_events
		WHERE event_id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))),
	hq(event_id, uid, node_id, op_type, payload) AS (
		SELECT e.event_id, COALESCE(e.uid, ''), e.node_id, e.op_type, e.payload
		FROM sync_quarantine q CROSS JOIN sync_events e ON e.event_id = q.event_id
		WHERE q.source = 'push' AND substr(q.reason, 1, length(?)) = ?),
	ev(side, event_id, uid, node_id, op_type, payload) AS (
		SELECT 'b', event_id, uid, node_id, op_type, payload FROM batch
		UNION ALL
		SELECT 'h', event_id, uid, node_id, op_type, payload FROM hq),
	start(side, event_id, id, uid, parent_id) AS (
		SELECT v.side, v.event_id, n.id, n.uid, n.parent_id FROM ev v
		JOIN nodes n ON n.uid = v.uid AND n.uid <> '' WHERE v.uid <> ''
		UNION
		SELECT v.side, v.event_id, n.id, COALESCE(n.uid, ''), n.parent_id FROM ev v
		JOIN nodes n ON n.id = v.node_id WHERE v.uid = ''
		UNION
		SELECT v.side, v.event_id, n.id, COALESCE(n.uid, ''), n.parent_id FROM ev v
		JOIN nodes n ON n.id = json_extract(v.payload, '$.depends_on_node_id')
		WHERE +v.op_type IN ('link_dep', 'unlink_dep') AND json_valid(v.payload)),
	line(side, event_id, id, uid, parent_id, depth) AS (
		SELECT side, event_id, id, uid, parent_id, 0 FROM start
		UNION ALL
		SELECT l.side, l.event_id, p.id, COALESCE(p.uid, ''), p.parent_id, l.depth + 1
		FROM line l JOIN nodes p ON p.id = l.parent_id WHERE l.depth < 64),
	named(uid) AS (
		SELECT uid FROM hq WHERE uid <> ''
		UNION
		SELECT uid FROM line WHERE side = 'h' AND uid <> ''),
	subj(event_id, rid, own_delete, node_id, uid) AS (
		SELECT event_id, rid, op_type = 'delete', node_id, uid FROM batch WHERE uid <> ''
		UNION
		SELECT b.event_id, b.rid, 0, l.id, l.uid FROM line l JOIN batch b ON b.event_id = l.event_id
		WHERE l.side = 'b' AND l.uid <> ''),
	held(event_id, rid, own_delete, node_id, uid, cu, ce, last_del) AS (
		SELECT s.event_id, s.rid, s.own_delete, s.node_id, s.uid,
		       (SELECT MIN(c.rowid) FROM sync_events c WHERE c.uid = s.uid AND c.uid <> '' AND +c.op_type = 'create_node'),
		       (SELECT MIN(c.rowid) FROM sync_events c WHERE c.event_id = s.uid AND +c.op_type = 'create_node'),
		       (SELECT MAX(d.rowid) FROM sync_events d WHERE d.uid = s.uid AND d.uid <> '' AND +d.op_type = 'delete')
		FROM subj s WHERE s.uid IN (SELECT uid FROM named)),
	logged(event_id, rid, own_delete, node_id, uid, cr, last_del) AS (
		SELECT event_id, rid, own_delete, node_id, uid, COALESCE(MIN(cu, ce), cu, ce), last_del FROM held)
	SELECT l.event_id, l.node_id, l.uid, 0 FROM logged l
	WHERE l.cr IS NOT NULL
	  AND ((l.own_delete AND l.rid < l.cr)
	       OR (l.last_del < l.cr
	           AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid = l.uid AND n.uid <> '' AND n.deleted_at IS NULL)))
	UNION
	SELECT b.event_id, b.node_id, b.uid, 1 FROM batch b JOIN sync_quarantine q ON q.event_id = b.event_id
	WHERE q.source = 'push' AND substr(q.reason, 1, length(?)) = ?
	UNION
	SELECT b.event_id, b.node_id, b.uid, 1 FROM batch b
	WHERE b.uid <> ''
	  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid = b.uid AND n.uid <> '')
	  AND NOT EXISTS (SELECT 1 FROM sync_events c WHERE c.uid = b.uid AND c.uid <> '' AND +c.op_type = 'create_node')
	  AND NOT EXISTS (SELECT 1 FROM sync_events c WHERE c.event_id = b.uid AND +c.op_type = 'create_node')`
}

// readRetiredUnlogged returns, keyed by event id, the retired subject of
// each event of the bound id list that retiredUnloggedQuery lists; a purged
// subject wins over another.
func readRetiredUnlogged(ctx context.Context, q pushQuerier, ids string) (map[string]UnloggedSubject, error) {
	rows, err := q.QueryContext(ctx, retiredUnloggedQuery(), ids,
		UnloggedHoldPrefix, UnloggedHoldPrefix, UnloggedPurgedPrefix, UnloggedPurgedPrefix)
	if err != nil {
		return nil, fmt.Errorf("query retired unlogged holds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]UnloggedSubject{}
	for rows.Next() {
		var eventID string
		s := UnloggedSubject{Retired: true}
		if err := rows.Scan(&eventID, &s.NodeID, &s.UID, &s.Purged); err != nil {
			return nil, fmt.Errorf("scan retired unlogged hold: %w", err)
		}
		if prev, seen := out[eventID]; !seen || (s.Purged && !prev.Purged) {
			out[eventID] = s
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read retired unlogged holds: %w", err)
	}
	return out, nil
}
