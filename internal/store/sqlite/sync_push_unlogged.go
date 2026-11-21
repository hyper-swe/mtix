// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// Reads for the push hold of tasks the sync log has no creation for
// (MTIX-112.17.16.37, founder decision DESK-1.371, option D-hold).
//
// A task can be on the board with no create_node in the sync log: a merge
// import made before the store first synced writes no journal, and neither
// did a task made before the journal existed. A peer never receives such a
// task, so it quarantines every later event about it. Push holds those
// events instead (cmd/mtix/sync_push_hold.go). An event is about the task
// that has its uid (by its number when it has no uid) and, for a link or
// unlink, also the task it points to; it depends on each of those tasks and
// every task above them. The log holds a task's creation when a create_node
// carries the task's uid, or has the uid as its event id (a creation queued
// before events carried uids, ADR-003 section 2), or, for a task without a
// uid, names its number with no uid. A pulled creation is mirrored in
// sync_events, so it counts; a pending or held one counts too.

// UnloggedSubject is the task an event depends on that the sync log has no
// creation for: the topmost such task on the event's lines, its uid, the
// number of the task above it (empty for a root) and whether the hold is
// retired. A hold is retired when, on every line of the event that holds an
// unlogged task, the event's own task and every task up to the topmost
// unlogged one are deleted locally (or purged): nothing of it is left to
// send, and peers never held it.
type UnloggedSubject struct {
	NodeID, UID, ParentID string
	Retired               bool
}

// lineTask is one task on one line of an event.
type lineTask struct {
	id, uid, parentID string
	depth             int
	deleted, logged   bool
}

// UnloggedSubjects returns, keyed by event id, the unlogged task each of
// eventIDs depends on, and, retired, the task of each event that stays
// local for good (retiredUnloggedQuery: held as unlogged, then deleted,
// purged or its delete held, even after its creation reached the log; or
// purged with no logged creation). An event that depends only on logged
// tasks otherwise, or has no sync_events row, is left out.
func (s *Store) UnloggedSubjects(ctx context.Context, eventIDs []string) (map[string]UnloggedSubject, error) {
	return unloggedSubjects(ctx, pushReadAdapter{s}, eventIDs)
}

// UnloggedSubjects is Store.UnloggedSubjects inside the lock.
func (l PushLocked) UnloggedSubjects(ctx context.Context, eventIDs []string) (map[string]UnloggedSubject, error) {
	return unloggedSubjects(ctx, l.tx, eventIDs)
}

// unloggedSubjects is UnloggedSubjects over q.
func unloggedSubjects(ctx context.Context, q pushQuerier, eventIDs []string) (map[string]UnloggedSubject, error) {
	out := map[string]UnloggedSubject{}
	if len(eventIDs) == 0 {
		return out, nil
	}
	ids, err := hexIDList(eventIDs)
	if err != nil {
		return nil, fmt.Errorf("read unlogged push subjects: %w", err)
	}
	lines, err := readEventLines(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("read unlogged push subjects: %w", err)
	}
	for eventID, byStart := range lines {
		if subj, ok := unloggedOf(byStart); ok {
			out[eventID] = subj
		}
	}
	// A task still unlogged on the event's lines comes first: an undeleted
	// one is named again (mtix undelete fails doctor again).
	retired, err := readRetiredUnlogged(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("read unlogged push subjects: %w", err)
	}
	for eventID, subj := range retired {
		if _, ok := out[eventID]; !ok {
			out[eventID] = subj
		}
	}
	return out, nil
}

// unloggedLinesQuery is the query readEventLines runs. For each event in
// the bound hex-encoded JSON array it lists every task on its lines (the
// task it is about, the target of a link or unlink, and the tasks above
// each, depth 0 first), whether the log holds the creation of each and
// whether each is deleted. Every step is a primary-key or index lookup
// (pinned by TestUnloggedLinesQuery_ExplainQueryPlan_SearchesIndexes): a
// task with a uid is found by idx_nodes_uid and its creation by
// idx_sync_events_uid (by the creation's uid) or the primary key (by its
// event id); a task without a uid is matched by number among the creations
// without a uid through idx_sync_events_node. The unary plus on op_type
// keeps the planner off idx_sync_events_op_type, which would read every
// creation for each task (and every link of the log for the batch), and
// the c.uid term lets the partial uid index apply (review r1 F3).
func unloggedLinesQuery() string {
	return `
	WITH RECURSIVE
	batch(event_id, uid, node_id, op_type, payload) AS (
		SELECT event_id, COALESCE(uid, ''), node_id, op_type, payload FROM sync_events
		WHERE event_id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))),
	start(event_id, id) AS (
		SELECT b.event_id, n.id FROM batch b JOIN nodes n ON n.uid = b.uid AND n.uid <> '' WHERE b.uid <> ''
		UNION
		SELECT b.event_id, n.id FROM batch b JOIN nodes n ON n.id = b.node_id WHERE b.uid = ''
		UNION
		SELECT b.event_id, n.id FROM batch b
		JOIN nodes n ON n.id = json_extract(b.payload, '$.depends_on_node_id')
		WHERE +b.op_type IN ('link_dep', 'unlink_dep') AND json_valid(b.payload)),
	line(event_id, start_id, id, parent_id, depth) AS (
		SELECT s.event_id, s.id, n.id, n.parent_id, 0 FROM start s JOIN nodes n ON n.id = s.id
		UNION ALL
		SELECT l.event_id, l.start_id, p.id, p.parent_id, l.depth + 1
		FROM line l JOIN nodes p ON p.id = l.parent_id WHERE l.depth < 64)
	SELECT l.event_id, l.start_id, n.id, COALESCE(n.uid, ''), COALESCE(n.parent_id, ''), l.depth,
	       n.deleted_at IS NOT NULL,
	       CASE WHEN COALESCE(n.uid, '') <> '' THEN
	            EXISTS (SELECT 1 FROM sync_events c WHERE c.uid = n.uid AND c.uid <> '' AND n.uid <> ''
	                    AND +c.op_type = 'create_node')
	         OR EXISTS (SELECT 1 FROM sync_events c WHERE c.event_id = n.uid AND n.uid <> ''
	                    AND +c.op_type = 'create_node')
	       ELSE EXISTS (SELECT 1 FROM sync_events c WHERE c.node_id = n.id AND +c.op_type = 'create_node'
	            AND COALESCE(c.uid, '') = '') END
	FROM line l JOIN nodes n ON n.id = l.id
	ORDER BY l.event_id, l.start_id, l.depth`
}

// readEventLines reads unloggedLinesQuery for the bound id list: per event,
// per line start, the line's tasks depth first.
func readEventLines(ctx context.Context, q pushQuerier, ids string) (map[string]map[string][]lineTask, error) {
	rows, err := q.QueryContext(ctx, unloggedLinesQuery(), ids)
	if err != nil {
		return nil, fmt.Errorf("query event lines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[string][]lineTask{}
	for rows.Next() {
		var eventID, startID string
		var t lineTask
		if err := rows.Scan(&eventID, &startID, &t.id, &t.uid, &t.parentID, &t.depth, &t.deleted, &t.logged); err != nil {
			return nil, fmt.Errorf("scan event line: %w", err)
		}
		if out[eventID] == nil {
			out[eventID] = map[string][]lineTask{}
		}
		out[eventID][startID] = append(out[eventID][startID], t)
	}
	return out, rows.Err()
}

// unloggedOf returns the topmost unlogged task on the lines of one event,
// with whether its hold is retired, and false when every task on them is
// logged.
func unloggedOf(byStart map[string][]lineTask) (UnloggedSubject, bool) {
	var top *lineTask
	retired := true
	for _, line := range byStart {
		upper := -1
		for i := range line {
			if !line[i].logged {
				upper = i
			}
		}
		if upper < 0 {
			continue
		}
		if top == nil || line[upper].depth > top.depth {
			top = &line[upper]
		}
		for _, t := range line[:upper+1] {
			retired = retired && t.deleted
		}
	}
	if top == nil {
		return UnloggedSubject{}, false
	}
	return UnloggedSubject{NodeID: top.id, UID: top.uid, ParentID: top.parentID, Retired: retired}, true
}

// SubjectsGone returns the ids among eventIDs whose task, or for a link or
// unlink whose target, has no node on this board any more (mtix gc purged
// it, or the event names a task that never existed here). A push hold of
// such an event whose task is unlogged is retired: nothing of the task is
// left to send. A held event whose subject is not gone and that depends on
// no unlogged task any more (its creation reached the log, for example by
// pull) is released instead (review r1 F1). An id with no sync_events row
// is left out.
func (s *Store) SubjectsGone(ctx context.Context, eventIDs []string) (map[string]bool, error) {
	gone := map[string]bool{}
	if len(eventIDs) == 0 {
		return gone, nil
	}
	ids, err := hexIDList(eventIDs)
	if err != nil {
		return nil, fmt.Errorf("read gone push subjects: %w", err)
	}
	// The events whose task (by uid through idx_nodes_uid, by number when the
	// event has no uid) or link target (by number) no node holds.
	rows, err := s.Query(ctx, `
	WITH batch(event_id, uid, node_id, op_type, payload) AS (
		SELECT event_id, COALESCE(uid, ''), node_id, op_type, payload FROM sync_events
		WHERE event_id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?)))
	SELECT b.event_id FROM batch b
	WHERE (b.uid <> '' AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid = b.uid AND n.uid <> ''))
	   OR (b.uid = '' AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.id = b.node_id))
	   OR (b.op_type IN ('link_dep', 'unlink_dep') AND json_valid(b.payload)
	       AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.id = json_extract(b.payload, '$.depends_on_node_id')))`,
		ids)
	if err != nil {
		return nil, fmt.Errorf("read gone push subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read gone push subjects: %w", err)
		}
		gone[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read gone push subjects: %w", err)
	}
	return gone, nil
}

// heldReasonIDsSQL lists the ids of the held push events whose reason
// starts with the bound prefix, in queue order.
const heldReasonIDsSQL = `
	SELECT q.event_id FROM sync_quarantine q
	LEFT JOIN sync_events e ON e.event_id = q.event_id
	WHERE q.source = 'push' AND substr(q.reason, 1, length(?1)) = ?1
	ORDER BY COALESCE(e.lamport_clock, 0), q.event_id`

// HeldPushEventIDsWithReason returns the ids of the held push events whose
// reason starts with prefix, in queue order: doctor reads the holds of
// unlogged tasks this way.
func (s *Store) HeldPushEventIDsWithReason(ctx context.Context, prefix string) ([]string, error) {
	rows, err := s.Query(ctx, heldReasonIDsSQL, prefix)
	if err != nil {
		return nil, fmt.Errorf("read held push events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read held push events: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read held push events: %w", err)
	}
	return ids, nil
}
