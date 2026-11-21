// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PurgeDeletedNode permanently deletes the soft-deleted node id (mtix gc,
// FR-3.3a); a node that is not deleted is left alone. In the same
// transaction, when the sync log holds no creation of the node, it marks as
// purged (UnloggedPurgedPrefix) every push hold of an unlogged task whose
// event has the node on its lines: its own events, those of the tasks below
// it, and links to or from them (MTIX-112.17.16.37, lead ruling r2 3). A
// purge counts as a delete made before any later logged creation, so those
// holds stay retired even after a pull brings a teammate's creation of the
// same task back here, and the teammate's task keeps its own content on
// every replica. Like the plain delete it replaces, it records no export
// request (local bookkeeping, withLocalTx).
func (s *Store) PurgeDeletedNode(ctx context.Context, id string) error {
	return s.withLocalTx(ctx, func(tx *sql.Tx) error {
		if err := markPurgedUnloggedHolds(ctx, tx, id); err != nil {
			return fmt.Errorf("purge node %s: %w", id, err)
		}
		// Remove the node while it is still soft-deleted (FR-3.3a).
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ? AND deleted_at IS NOT NULL`, id); err != nil {
			return fmt.Errorf("purge node %s: %w", id, err)
		}
		return nil
	})
}

// markPurgedUnloggedHolds marks the push holds PurgeDeletedNode names for
// the soft-deleted node id, when the log holds no creation of it.
func markPurgedUnloggedHolds(ctx context.Context, tx *sql.Tx, id string) error {
	var uid string
	var logged bool
	// The soft-deleted node and whether the log holds its creation: by the
	// creation's uid or its event id for a node with a uid, by number among
	// the creations without a uid otherwise (as unloggedLinesQuery).
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(n.uid, ''),
		       CASE WHEN COALESCE(n.uid, '') <> '' THEN
		            EXISTS (SELECT 1 FROM sync_events c WHERE c.uid = n.uid AND c.uid <> '' AND n.uid <> ''
		                    AND +c.op_type = 'create_node')
		         OR EXISTS (SELECT 1 FROM sync_events c WHERE c.event_id = n.uid AND n.uid <> ''
		                    AND +c.op_type = 'create_node')
		       ELSE EXISTS (SELECT 1 FROM sync_events c WHERE c.node_id = n.id AND +c.op_type = 'create_node'
		            AND COALESCE(c.uid, '') = '') END
		FROM nodes n WHERE n.id = ? AND n.deleted_at IS NOT NULL`, id).Scan(&uid, &logged)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && logged) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read purged node: %w", err)
	}
	// Every push hold of an unlogged task not marked yet, the tasks on the
	// lines of its event (its own task by uid, or by number without a uid, a
	// link's target, and the tasks above each); the holds whose lines hold
	// the node take the purged reason.
	_, err = tx.ExecContext(ctx, `
		WITH RECURSIVE
		hq(event_id, uid, node_id, op_type, payload) AS (
			SELECT e.event_id, COALESCE(e.uid, ''), e.node_id, e.op_type, e.payload
			FROM sync_quarantine q CROSS JOIN sync_events e ON e.event_id = q.event_id
			WHERE q.source = 'push' AND substr(q.reason, 1, length(?2)) = ?2
			  AND substr(q.reason, 1, length(?3)) <> ?3),
		start(event_id, id, parent_id) AS (
			SELECT h.event_id, n.id, n.parent_id FROM hq h JOIN nodes n ON n.uid = h.uid AND n.uid <> '' WHERE h.uid <> ''
			UNION
			SELECT h.event_id, n.id, n.parent_id FROM hq h JOIN nodes n ON n.id = h.node_id WHERE h.uid = ''
			UNION
			SELECT h.event_id, n.id, n.parent_id FROM hq h
			JOIN nodes n ON n.id = json_extract(h.payload, '$.depends_on_node_id')
			WHERE +h.op_type IN ('link_dep', 'unlink_dep') AND json_valid(h.payload)),
		line(event_id, id, parent_id, depth) AS (
			SELECT event_id, id, parent_id, 0 FROM start
			UNION ALL
			SELECT l.event_id, p.id, p.parent_id, l.depth + 1 FROM line l JOIN nodes p ON p.id = l.parent_id
			WHERE l.depth < 64)
		UPDATE sync_quarantine SET reason = ?4
		WHERE source = 'push' AND event_id IN (SELECT event_id FROM line WHERE id = ?1)`,
		id, UnloggedHoldPrefix, UnloggedPurgedPrefix, UnloggedPurgedReason(id, uid))
	if err != nil {
		return fmt.Errorf("mark purged holds: %w", err)
	}
	return nil
}
