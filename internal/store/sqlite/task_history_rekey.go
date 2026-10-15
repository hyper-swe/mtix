// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// A task without a uid keys its history on its dot-path id. When such a task
// is renumbered, renamed or given a uid, its history is rekeyed in the same
// transaction so one task keeps one key (MTIX-95.31.19.3). A task with a uid
// never needs this: history keys on the uid, which no rewrite changes.
//
// Rekeying is collision-safe: the unique (task_uid, event_id, field) index
// would refuse a move onto a key that already holds the same event's entry,
// and that entry is the same one, so the old-key duplicate is dropped first.

// rekeyHistoryExact moves the history keyed oldKey to newKey.
func rekeyHistoryExact(ctx context.Context, tx *sql.Tx, oldKey, newKey string) error {
	if oldKey == newKey {
		return nil
	}
	// Drop the rows of the old key whose event the new key already records.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM task_history
		  WHERE task_uid = ? AND event_id IS NOT NULL
		    AND EXISTS (SELECT 1 FROM task_history n
		                 WHERE n.task_uid = ? AND n.event_id = task_history.event_id AND n.field = task_history.field)`,
		oldKey, newKey); err != nil {
		return fmt.Errorf("rekey history %s -> %s: drop duplicates: %w", oldKey, newKey, err)
	}
	// Rows of a task keyed by its id move with the id.
	if _, err := tx.ExecContext(ctx,
		`UPDATE task_history SET task_uid = ? WHERE task_uid = ?`, newKey, oldKey); err != nil {
		return fmt.Errorf("rekey history %s -> %s: %w", oldKey, newKey, err)
	}
	return nil
}

// rekeyHistorySubtree moves the history of oldID and of every descendant id
// to the same ids under newID, by the prefix substitution renumber applies to
// the nodes table. The descendant range is an id range ('.' to '/'), which the
// task_uid index serves.
func rekeyHistorySubtree(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	// Drop the old-key rows whose substituted key already records the same event.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM task_history
		  WHERE (task_uid = ? OR (task_uid >= ? AND task_uid < ?)) AND event_id IS NOT NULL
		    AND EXISTS (SELECT 1 FROM task_history n
		                 WHERE n.task_uid = ? || SUBSTR(task_history.task_uid, LENGTH(?) + 1)
		                   AND n.event_id = task_history.event_id AND n.field = task_history.field)`,
		oldID, oldID+".", oldID+"/", newID, oldID); err != nil {
		return fmt.Errorf("rekey history subtree %s -> %s: drop duplicates: %w", oldID, newID, err)
	}
	// Rows keyed by the subtree's ids: the root, and "oldID." descendants.
	if _, err := tx.ExecContext(ctx,
		`UPDATE task_history SET task_uid = ? || SUBSTR(task_uid, LENGTH(?) + 1)
		  WHERE task_uid = ? OR (task_uid >= ? AND task_uid < ?)`,
		newID, oldID, oldID, oldID+".", oldID+"/"); err != nil {
		return fmt.Errorf("rekey history subtree %s -> %s: %w", oldID, newID, err)
	}
	return nil
}

// rekeyFilledHistory moves the history keyed by each of ids to the uid the node
// holds now, for the nodes a backfill just filled. A node still without a uid
// keeps its key, and no other key moves.
func rekeyFilledHistory(ctx context.Context, tx *sql.Tx, ids []string) error {
	var hasHistory bool
	// A store with no history (the usual pre-v3 migration) has nothing to rekey.
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM task_history)`).Scan(&hasHistory); err != nil {
		return fmt.Errorf("probe task_history before rekey: %w", err)
	}
	if !hasHistory {
		return nil
	}
	for _, id := range ids {
		var uid sql.NullString
		// The uid the node holds after the backfill.
		if err := tx.QueryRowContext(ctx, `SELECT uid FROM nodes WHERE id = ?`, id).Scan(&uid); err != nil {
			return fmt.Errorf("read uid of %s after the backfill: %w", id, err)
		}
		if uid.Valid && uid.String != "" {
			if err := rekeyHistoryExact(ctx, tx, id, uid.String); err != nil {
				return err
			}
		}
	}
	return nil
}
