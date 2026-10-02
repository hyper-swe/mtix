// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// carryAdoptedUID moves every local reference to the uid a task gives up
// onto the uid it adopts (MTIX-95.31.16, FR-7.8, FR-18.6), in the merge
// import's transaction. Invariant: after a merge, no unpushed local record
// names the task by a uid the task no longer has, so a push, or the release
// of a held creation, never sends one task's changes under two uids. The
// references are:
//   - the uid column of the task's pending sync_events, of every op kind
//     (a create_node included, whose uid no longer equals its event id: the
//     hub then finds the task the file's uid names and follows its
//     collision rules), and of a pending event that names the task by its
//     number and carries no uid (queued before events carried one);
//   - the uid inside the raw event of the task's held push events in
//     sync_quarantine (the copy HoldPushEvents stored);
//   - the task's pending creation itself: a creation's uid is its own
//     event id (ADR-003 §2), and the hub names a creation by that id (a
//     renumber-required outcome), so the creation takes the adopted uid as
//     its event id too, in sync_events and in its held copy, and the reason
//     of every held event that names it (renameAdoptedCreation).
//
// Every write that changes the uid of a task that had one runs it: the merge
// import (mergeImportNode, mergeUnchangedContent) and the replace import
// (carryReplacedUIDs); the paths that only give a task its first uid change
// no uid a reference could name.
// The caller must keep every push away while the carry runs
// (ImportReconcileOptions.HoldPush, the push lock): a push in flight would
// otherwise mark as pushed, by their old ids, events the carry renamed, and
// send them again under the new uid. Events already pushed are history and stay. Payloads carry numbers
// (parent_id, depends_on_node_id), never uids, so a child's creation and a
// link follow their task through the number; sync_events.uid and the held
// raw event are the only uid-bearing records beside nodes.uid. nodeID is
// the task's number, oldUID the uid it gives up (may be empty), newUID the
// uid it takes (never empty).
func carryAdoptedUID(ctx context.Context, tx *sql.Tx, nodeID, oldUID, newUID string) error {
	if newUID == "" || newUID == oldUID {
		return nil
	}
	args := []any{sql.Named("new", newUID), sql.Named("node", nodeID), sql.Named("old", oldUID)}
	// The held copies first, while the old uid still marks the events: the
	// raw event of each held push event of the task takes the new uid.
	// Bound by name: @new the adopted uid, @node the number, @old the uid
	// given up. The task's pending events: those with the old uid, and
	// those with none that name the number.
	if _, err := tx.ExecContext(ctx, `
		UPDATE sync_quarantine SET raw_event = json_set(raw_event, '$.uid', @new)
		WHERE source = 'push' AND json_valid(raw_event) AND event_id IN (
		  SELECT event_id FROM sync_events
		  WHERE sync_status = 'pending'
		    AND ((@old <> '' AND uid = @old) OR ((uid IS NULL OR uid = '') AND node_id = @node)))`,
		args...); err != nil {
		return fmt.Errorf("carry the adopted uid onto the held events of %s: %w", nodeID, err)
	}
	// The task's pending events take the new uid, selected the same way.
	if _, err := tx.ExecContext(ctx, `
		UPDATE sync_events SET uid = @new
		WHERE sync_status = 'pending'
		  AND ((@old <> '' AND uid = @old) OR ((uid IS NULL OR uid = '') AND node_id = @node))`,
		args...); err != nil {
		return fmt.Errorf("carry the adopted uid onto the pending events of %s: %w", nodeID, err)
	}
	return renameAdoptedCreation(ctx, tx, nodeID, oldUID, newUID)
}

// renameAdoptedCreation gives the task's pending creation, whose event id is
// the uid the task gave up, the adopted uid as its event id (MTIX-95.31.16,
// ADR-003 §2: uid == the creation's event id), and updates what names the
// old id: its held copy (the row and the raw event's event_id) and the
// reason ("depends on held create of <event id>") of every held push event.
// It does nothing when the task has no such creation, or when a pulled or
// held event already has the adopted uid as its id: the creation then keeps
// its id and only its uid column changes.
func renameAdoptedCreation(ctx context.Context, tx *sql.Tx, nodeID, oldUID, newUID string) error {
	if oldUID == "" {
		return nil
	}
	var free int
	// 1 when a pending creation has the old uid as its event id and no
	// event or held row already has the new uid as its id.
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_events c
		WHERE c.event_id = ? AND c.op_type = 'create_node' AND c.sync_status = 'pending'
		  AND NOT EXISTS (SELECT 1 FROM sync_events WHERE event_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM sync_quarantine WHERE event_id = ?)`,
		oldUID, newUID, newUID).Scan(&free); err != nil {
		return fmt.Errorf("find the creation of %s to re-identify: %w", nodeID, err)
	}
	if free == 0 {
		return nil
	}
	stmts := []string{
		// The held copy: its row and the event id inside its raw event.
		`UPDATE sync_quarantine SET event_id = @new,
		   raw_event = CASE WHEN json_valid(raw_event) THEN json_set(raw_event, '$.event_id', @new) ELSE raw_event END
		 WHERE source = 'push' AND event_id = @old`,
		// The reasons that name the creation by its old event id.
		`UPDATE sync_quarantine SET reason = REPLACE(reason, @old, @new)
		 WHERE source = 'push' AND INSTR(reason, @old) > 0`,
		// The creation itself.
		`UPDATE sync_events SET event_id = @new WHERE event_id = @old AND op_type = 'create_node'`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt, sql.Named("old", oldUID), sql.Named("new", newUID)); err != nil {
			return fmt.Errorf("re-identify the creation of %s: %w", nodeID, err)
		}
	}
	return nil
}

// localTask is the uid and title a local node holds before a replace.
type localTask struct{ uid, title string }

// localUIDsByID reads the uid and title of every local node that has a uid,
// keyed by id, soft-deleted ones included, before a replace clears them.
func localUIDsByID(ctx context.Context, tx *sql.Tx) (map[string]localTask, error) {
	// Every local node's id, uid and title, for the nodes that carry a uid.
	rows, err := tx.QueryContext(ctx, `SELECT id, uid, title FROM nodes WHERE uid IS NOT NULL AND uid <> ''`)
	if err != nil {
		return nil, fmt.Errorf("read the local uids before the replace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]localTask)
	for rows.Next() {
		var id string
		var t localTask
		if err := rows.Scan(&id, &t.uid, &t.title); err != nil {
			return nil, fmt.Errorf("read the local uids before the replace: %w", err)
		}
		out[id] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the local uids before the replace: %w", err)
	}
	return out, nil
}

// carryReplacedUIDs applies carryAdoptedUID to every task a replace import
// leaves under the same id and title but another uid (MTIX-95.31.16): the
// automatic import after a git pull adopts the file's uid for a same-title
// task whose uid was assigned at upgrade. A task whose title differs is a
// different task: its pending events are not moved onto the file's task.
// The caller holds the push lock.
func carryReplacedUIDs(ctx context.Context, tx *sql.Tx, before map[string]localTask, data *ExportData) error {
	for i := range data.Nodes {
		n := &data.Nodes[i]
		old, held := before[n.ID]
		if !held || n.UID == "" || n.UID == old.uid || n.Title != old.title {
			continue
		}
		if err := carryAdoptedUID(ctx, tx, n.ID, old.uid, n.UID); err != nil {
			return err
		}
	}
	return nil
}
