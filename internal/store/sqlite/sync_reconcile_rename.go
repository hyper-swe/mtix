// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// executeRenameTx is the body of RenameTo's tx. Extracted to keep
// the outer function below the cognitive-complexity lint threshold.
//
// Defers FK checks to commit time so per-row UPDATE on parent_id
// references doesn't fail before the new ids are populated.
func executeRenameTx(
	ctx context.Context, s *Store, tx *sql.Tx,
	mtixDir string, mapping map[string]string, newPrefix string,
) (int, error) {
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return 0, fmt.Errorf("defer FK: %w", err)
	}
	count, err := applyRenameLoop(ctx, s, tx, mtixDir, mapping)
	if err != nil {
		return count, err
	}
	if err := realignMovedColumns(ctx, tx, mapping); err != nil { // MTIX-107.58
		return count, err
	}
	if err := updateSentinelsAfterReconcile(ctx, tx, newPrefix); err != nil {
		return count, err
	}
	return count, advanceRenamedCounters(ctx, tx, newPrefix) // MTIX-95.38
}

// applyRenameLoop iterates the rename mapping in long-id-first order
// and applies each rename + its audit event. The long-id-first order
// is belt-and-suspenders alongside deferred FKs.
func applyRenameLoop(
	ctx context.Context, s *Store, tx *sql.Tx,
	mtixDir string, mapping map[string]string,
) (int, error) {
	oldIDs := make([]string, 0, len(mapping))
	for k := range mapping {
		oldIDs = append(oldIDs, k)
	}
	sort.Slice(oldIDs, func(i, j int) bool {
		return len(oldIDs[i]) > len(oldIDs[j])
	})

	count := 0
	for _, oldID := range oldIDs {
		newID := mapping[oldID]
		if err := renameNodeRow(ctx, tx, oldID, newID); err != nil {
			return count, fmt.Errorf("rename %s -> %s: %w", oldID, newID, err)
		}
		audit := auditEvent{
			Type:      "RENAME_NODE",
			Timestamp: s.clock().UTC().Format(time.RFC3339Nano),
			OldID:     oldID,
			NewID:     newID,
		}
		if err := appendAuditEvent(mtixDir, audit); err != nil {
			return count, fmt.Errorf("audit rename %s: %w", oldID, err)
		}
		count++
		// Chaos hook for atomicity tests (15.6.3). Production runs
		// leave this nil; the test sets it to inject a failure after
		// the Nth successful rename so the surrounding WithTx rolls
		// back.
		if reconcileFailAfterN != nil {
			if injectErr := reconcileFailAfterN(count); injectErr != nil {
				return count, fmt.Errorf("chaos: %w", injectErr)
			}
		}
	}
	return count, nil
}

// updateSentinelsAfterReconcile rewrites meta.sync.project_prefix
// to the new value, clears the cached first_event_hash, and drops
// sync_projects so GetOrComputeLocalFirstEventHash recomputes on the
// renamed tree.
func updateSentinelsAfterReconcile(ctx context.Context, tx *sql.Tx, newPrefix string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.project_prefix'`,
		newPrefix,
	); err != nil {
		return fmt.Errorf("update prefix sentinel: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = '' WHERE key = 'meta.sync.first_event_hash'`,
	); err != nil {
		return fmt.Errorf("invalidate cached hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_projects`,
	); err != nil {
		return fmt.Errorf("clear sync_projects: %w", err)
	}
	return nil
}

// renameNodeRow updates every reference to oldID to newID in the
// nodes, dependencies, sync_events, and sync_conflicts tables. Called
// inside the caller's tx.
func renameNodeRow(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	for _, stmt := range []struct{ sql, what string }{
		{`UPDATE nodes SET id = ? WHERE id = ?`, "nodes.id"},
		{`UPDATE nodes SET parent_id = ? WHERE parent_id = ?`, "nodes.parent_id"},
		{`UPDATE dependencies SET from_id = ? WHERE from_id = ?`, "dependencies.from_id"},
		{`UPDATE dependencies SET to_id = ? WHERE to_id = ?`, "dependencies.to_id"},
		{`UPDATE sync_events SET node_id = ? WHERE node_id = ?`, "sync_events.node_id"},
		{`UPDATE sync_conflicts SET node_id = ? WHERE node_id = ?`, "sync_conflicts.node_id"},
	} {
		if _, err := tx.ExecContext(ctx, stmt.sql, newID, oldID); err != nil {
			return fmt.Errorf("rename %s %s -> %s: %w", stmt.what, oldID, newID, err)
		}
	}
	// MTIX-95.31.19.3: a task keyed by its id keeps its history under the new id.
	return rekeyHistoryExact(ctx, tx, oldID, newID)
}

// recordRename runs a bulk id rename (RenameTo, ImportAs) inside one history
// transaction: every renamed task records its new display path and the parent
// id that followed, and a task with no uid pairs with its old state through
// the rename mapping (MTIX-95.31.19.3).
func (s *Store) recordRename(ctx context.Context, tx *sql.Tx, mapping map[string]string,
	origin model.HistoryOrigin, fn func() (int, error)) (count int, err error) {
	err = s.inTxHistory(ctx, tx, historyScope{all: true, origin: origin}, func() error {
		s.noteRenames(tx, mapping)
		var fnErr error
		count, fnErr = fn()
		return fnErr
	})
	return count, err
}
