// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// DeleteNode soft-deletes a node per FR-3.3.
// Sets deleted_at and deleted_by on the target node, stamped from the
// store's injected clock.
// When cascade is true (default), all descendants are also soft-deleted.
// In the same transaction it records which delete removed each node
// (MTIX-95.18): the target gets a cascade_deletes row naming itself, and
// cascadeDelete gives every descendant it removes a row naming the target.
// Each row is stamped with the node's deleted_at and deleted_by, so
// UndeleteNode can restore exactly what this delete removed, and can tell
// when a row no longer describes the node's current delete.
// Recalculates parent progress excluding the deleted subtree per FR-5.7.
//
// Returns ErrNotFound if the node does not exist or is already deleted.
func (s *Store) DeleteNode(ctx context.Context, id string, cascade bool, deletedBy string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		now := s.clock().UTC().Format(time.RFC3339)

		parentID, err := loadDeleteTarget(ctx, tx, id)
		if err != nil {
			return err
		}

		// Soft-delete the target node.
		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ?
			 WHERE id = ? AND deleted_at IS NULL`,
			now, deletedBy, now, id,
		)
		if err != nil {
			return fmt.Errorf("soft-delete node %s: %w", id, err)
		}

		// MTIX-95.18: the target was removed by its own delete.
		if err := recordCascadeRoot(ctx, tx, id, deletedBy, now); err != nil {
			return err
		}

		// Cascade to descendants if requested.
		if cascade {
			if err := cascadeDelete(ctx, tx, id, deletedBy, now); err != nil {
				return fmt.Errorf("cascade delete from %s: %w", id, err)
			}
		}

		payload, _ := model.EncodePayload(&model.DeletePayload{})
		if err := emitEvent(ctx, tx, emitParams{
			NodeID:      id,
			ProjectCode: projectPrefixFromNodeID(id),
			OpType:      model.OpDelete,
			Author:      deletedBy,
			Payload:     payload,
		}); err != nil {
			return err
		}

		// FR-5.7: Recalculate parent progress.
		if parentID.Valid && parentID.String != "" {
			if err := recalculateProgress(ctx, tx, parentID.String); err != nil {
				return fmt.Errorf("recalculate progress after delete: %w", err)
			}
		}

		return nil
	})
}

// loadDeleteTarget returns the parent of the live node id, which DeleteNode
// is about to soft-delete.
//
// Returns ErrNotFound if the node does not exist or is already deleted.
func loadDeleteTarget(ctx context.Context, tx *sql.Tx, id string) (sql.NullString, error) {
	var parentID sql.NullString
	// Verify node exists and is not already deleted.
	err := tx.QueryRowContext(ctx,
		`SELECT parent_id FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return parentID, fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return parentID, fmt.Errorf("check node %s: %w", id, err)
	}
	return parentID, nil
}

// recordCascadeRoot records, in the caller's transaction, that the delete of
// id removed id itself: its cascade_deletes row names id as the root and is
// stamped with the deleted_at and deleted_by the delete just wrote
// (MTIX-95.18). An existing row for id is overwritten, root and stamp: a node
// being deleted is live, so any row it still carries is stale, left by a
// restore that did not clear it (an older binary's undelete, an import).
func recordCascadeRoot(ctx context.Context, tx *sql.Tx, id, deletedBy, now string) error {
	// Upsert the stamped provenance row of the delete target.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cascade_deletes (node_id, root_id, deleted_at, deleted_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET root_id = excluded.root_id,
		     deleted_at = excluded.deleted_at, deleted_by = excluded.deleted_by`,
		id, id, now, deletedBy,
	); err != nil {
		return fmt.Errorf("record cascade root of %s: %w", id, err)
	}
	return nil
}

// cascadeDelete soft-deletes all descendants of a node per FR-3.3.
// Selects the subtree with a LIKE pattern on the dot-notation IDs. The
// parent ID is escaped (escapeLIKEPrefix) so a '_' in its project prefix
// matches literally and never reaches another project (MTIX-95.17).
//
// Before the rows change it records, in the same transaction, that this
// delete (the cascade root parentID) removed each live descendant, stamped
// with the deleted_at and deleted_by it is about to write (MTIX-95.18). A
// descendant that is already deleted was removed by an earlier delete; it
// keeps that delete's row and is not restored when parentID is undeleted.
func cascadeDelete(ctx context.Context, tx *sql.Tx, parentID, deletedBy, now string) error {
	descendants := escapeLIKEPrefix(parentID) + ".%"

	// Record the stamped cascade root of every live descendant (escaped
	// prefix, parameterized, ESCAPE '\'). Stale rows on live nodes are
	// overwritten, root and stamp. The WHERE clause also keeps SQLite from
	// reading ON CONFLICT as a join constraint of the SELECT.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cascade_deletes (node_id, root_id, deleted_at, deleted_by)
		 SELECT id, ?, ?, ? FROM nodes
		  WHERE id LIKE ? ESCAPE '\' AND deleted_at IS NULL
		 ON CONFLICT(node_id) DO UPDATE SET root_id = excluded.root_id,
		     deleted_at = excluded.deleted_at, deleted_by = excluded.deleted_by`,
		parentID, now, deletedBy, descendants,
	); err != nil {
		return fmt.Errorf("record cascade of %s: %w", parentID, err)
	}

	// Soft-delete all live descendants: every id that starts with the
	// literal "<parentID>." (escaped prefix, parameterized, ESCAPE '\').
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ?
		 WHERE id LIKE ? ESCAPE '\' AND deleted_at IS NULL`,
		now, deletedBy, now, descendants,
	)
	if err != nil {
		return fmt.Errorf("cascade delete descendants of %s: %w", parentID, err)
	}

	return nil
}

// UndeleteNode restores a soft-deleted node and the descendants that the same
// delete removed, per FR-3.3 (MTIX-95.18, review F-61). Clears deleted_at and
// deleted_by, stamping updated_at from the store's injected clock.
//
// The cascade_deletes row of id names the delete that removed it (its
// cascade root), and decides which descendants come back. A row is trusted
// only while its deleted_at and deleted_by stamp equals the node's current
// deleted_at and deleted_by. An older 0.5.x binary restores without clearing
// rows and deletes without writing them, and an import or a sync-applied
// delete leaves rows untouched, so a row whose stamp no longer matches
// describes an earlier delete and the node is treated as having no row:
//   - Undeleting a cascade root restores every descendant its cascade
//     removed. A descendant deleted on its own before the cascade, alone or
//     by its own cascade, names that other delete and stays deleted, even
//     when both deletes carry the same deleted_at second and author.
//   - Undeleting a node X that a still-deleted ancestor's cascade removed
//     restores X and the descendants of X that the same delete removed. The
//     ancestor and its other descendants stay deleted until it is undeleted.
//   - Fallback for a node with no trusted row: it was deleted before cascade
//     rows were recorded (MTIX-95.18), by an older binary, or by a path that
//     records none (a delete applied by sync, an import). The descendants
//     that have no trusted row either and whose deleted_at and deleted_by
//     equal the node's own are restored, which is how an older binary's
//     cascade looks. It cannot tell apart a descendant deleted on its own in
//     the same second by the same author. A descendant with a trusted row is
//     never restored by the fallback.
//
// Restored nodes lose their rows. The parent of every restored node, the
// parent of id included, has its progress recomputed (FR-5.7); restored
// leaves keep their own progress.
//
// Sync note (MTIX-15.2.3; ADR-006 census D10): UndeleteNode still does NOT
// emit a sync_events row, so other replicas keep the node deleted. Tombstones are monotonic per SYNC-DESIGN section 8.3 —
// a delete event once applied stays applied. Local restore is a single-CLI
// convenience (the row is recovered from the same DB it never left);
// cross-CLI restore must be done by a fresh create_node event under a new ID.
// An undelete event belongs to the sync redesign's projector phase.
//
// Returns ErrNotFound if the node does not exist or is not deleted.
func (s *Store) UndeleteNode(ctx context.Context, id string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		now := s.clock().UTC().Format(time.RFC3339)

		target, err := loadUndeleteTarget(ctx, tx, id)
		if err != nil {
			return err
		}

		// MTIX-95.18: choose the descendants that come back with id.
		var restored []restoredNode
		if target.rootID.Valid {
			// Recorded: exactly those the delete named by id's row removed.
			restored, err = selectRecordedCascade(ctx, tx, id, target.rootID.String)
		} else {
			// No row (pre-upgrade cascade): fall back to the unrecorded
			// descendants whose deleted_at and deleted_by match id's own.
			restored, err = selectUnrecordedCascade(ctx, tx, target)
		}
		if err != nil {
			return err
		}
		restored = append(restored, restoredNode{id: id, parentID: target.parentID})

		if err := restoreNodes(ctx, tx, restored, now); err != nil {
			return err
		}

		// FR-5.7: recompute the parent of every restored node, id's parent
		// included.
		return recalculateRestoredParents(ctx, tx, restored)
	})
}

// undeleteTarget is the deleted node UndeleteNode restores, with the fields
// that decide which descendants come back with it (MTIX-95.18).
type undeleteTarget struct {
	id        string
	parentID  string
	deletedAt string
	deletedBy sql.NullString
	rootID    sql.NullString // the delete that removed id; NULL when unrecorded
}

// restoredNode is one node an undelete restores, with its parent so the
// parent's progress can be recomputed (FR-5.7).
type restoredNode struct {
	id       string
	parentID string
}

// loadUndeleteTarget reads the deleted node id and the cascade_deletes row
// naming the delete that removed it, if that row is trusted: its stamp must
// equal the node's current deleted_at and deleted_by (MTIX-95.18).
//
// Returns ErrNotFound if the node does not exist or is not deleted.
func loadUndeleteTarget(ctx context.Context, tx *sql.Tx, id string) (undeleteTarget, error) {
	target := undeleteTarget{id: id}
	var parentID sql.NullString
	// The deleted node, LEFT JOINed to its provenance row when the row's
	// stamp matches the node's current delete: root_id is NULL when the
	// delete that removed it recorded no row or the row is stale.
	err := tx.QueryRowContext(ctx,
		`SELECT n.parent_id, n.deleted_at, n.deleted_by, c.root_id
		   FROM nodes n LEFT JOIN cascade_deletes c
		     ON c.node_id = n.id
		    AND c.deleted_at = n.deleted_at AND c.deleted_by IS n.deleted_by
		  WHERE n.id = ? AND n.deleted_at IS NOT NULL`,
		id,
	).Scan(&parentID, &target.deletedAt, &target.deletedBy, &target.rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return target, fmt.Errorf("deleted node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return target, fmt.Errorf("check deleted node %s: %w", id, err)
	}
	target.parentID = parentID.String
	return target, nil
}

// selectRecordedCascade returns the deleted descendants of id whose trusted
// cascade_deletes row names rootID, the delete that removed id (MTIX-95.18).
// A row is trusted while its stamp equals the descendant's current
// deleted_at and deleted_by.
// The subtree pattern escapes id (escapeLIKEPrefix) so a '_' in its project
// prefix cannot reach another project's nodes (MTIX-95.17).
func selectRecordedCascade(ctx context.Context, tx *sql.Tx, id, rootID string) ([]restoredNode, error) {
	// Deleted descendants of id whose current delete is the one rooted at
	// rootID (row stamp equal to the node's deleted_at and deleted_by).
	nodes, err := queryRestoreSet(ctx, tx,
		`SELECT n.id, COALESCE(n.parent_id, '')
		   FROM nodes n JOIN cascade_deletes c
		     ON c.node_id = n.id
		    AND c.deleted_at = n.deleted_at AND c.deleted_by IS n.deleted_by
		  WHERE n.id LIKE ? ESCAPE '\' AND n.deleted_at IS NOT NULL
		    AND c.root_id = ?`,
		escapeLIKEPrefix(id)+".%", rootID,
	)
	if err != nil {
		return nil, fmt.Errorf("select descendants of %s removed by %s: %w", id, rootID, err)
	}
	return nodes, nil
}

// selectUnrecordedCascade is the documented fallback for a target with no
// trusted cascade_deletes row (MTIX-95.18): the deleted descendants that have
// no trusted row either (none, or one whose stamp no longer equals their
// current deleted_at and deleted_by) and whose deleted_at and deleted_by
// equal the target's own, the signature of a cascade made before rows were
// recorded or by an older binary. deleted_by is
// compared with IS so a NULL author matches only NULL. The subtree pattern is
// escaped (escapeLIKEPrefix, MTIX-95.17).
func selectUnrecordedCascade(ctx context.Context, tx *sql.Tx, target undeleteTarget) ([]restoredNode, error) {
	// Descendants with no trusted row, deleted in the same second by the
	// same author.
	nodes, err := queryRestoreSet(ctx, tx,
		`SELECT n.id, COALESCE(n.parent_id, '')
		   FROM nodes n
		  WHERE n.id LIKE ? ESCAPE '\'
		    AND n.deleted_at = ? AND n.deleted_by IS ?
		    AND NOT EXISTS (SELECT 1 FROM cascade_deletes c
		                     WHERE c.node_id = n.id AND c.deleted_at = n.deleted_at
		                       AND c.deleted_by IS n.deleted_by)`,
		escapeLIKEPrefix(target.id)+".%", target.deletedAt, target.deletedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("select unrecorded cascade under %s: %w", target.id, err)
	}
	return nodes, nil
}

// queryRestoreSet runs a restore-set query, whose rows are (id, parent_id),
// in the caller's transaction and collects the result (MTIX-95.18). The
// query is one of the literal statements above; every value is bound.
func queryRestoreSet(ctx context.Context, tx *sql.Tx, query string, args ...any) (nodes []restoredNode, err error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close restore set: %w", closeErr)
		}
	}()
	for rows.Next() {
		var n restoredNode
		if scanErr := rows.Scan(&n.id, &n.parentID); scanErr != nil {
			return nil, fmt.Errorf("scan restore set: %w", scanErr)
		}
		nodes = append(nodes, n)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iterate restore set: %w", iterErr)
	}
	return nodes, nil
}

// restoreNodes clears deleted_at and deleted_by on each node and removes its
// cascade_deletes row: a live node has no delete to name (MTIX-95.18).
func restoreNodes(ctx context.Context, tx *sql.Tx, nodes []restoredNode, now string) error {
	for _, n := range nodes {
		// Restore one node.
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET deleted_at = NULL, deleted_by = NULL, updated_at = ?
			 WHERE id = ?`,
			now, n.id,
		); err != nil {
			return fmt.Errorf("undelete node %s: %w", n.id, err)
		}
		// Drop its provenance row.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM cascade_deletes WHERE node_id = ?`, n.id,
		); err != nil {
			return fmt.Errorf("clear cascade row of %s: %w", n.id, err)
		}
	}
	return nil
}

// recalculateRestoredParents recomputes, per FR-5.7, the progress of each
// distinct parent of a restored node (MTIX-95.18). recalculateProgress walks
// on up to the root, so the order does not matter: an ancestor's last
// recomputation always follows its children's. A parent that is still
// deleted keeps its stored progress (recalculateProgress skips it), and
// restored leaves are never recomputed, so they keep their own progress.
func recalculateRestoredParents(ctx context.Context, tx *sql.Tx, nodes []restoredNode) error {
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.parentID == "" || seen[n.parentID] {
			continue
		}
		seen[n.parentID] = true
		if err := recalculateProgress(ctx, tx, n.parentID); err != nil {
			return fmt.Errorf("recalculate progress of %s after undelete: %w", n.parentID, err)
		}
	}
	return nil
}
