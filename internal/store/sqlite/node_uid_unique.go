// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// uidIndexDDL is the one index over nodes.uid: UNIQUE over every non-empty
// uid, soft-deleted nodes included, because a uid names one task for good
// (ADR-003). Partial, so NULL and ” (a node not yet given a uid) never conflict.
const uidIndexDDL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_uid ON nodes(uid) WHERE uid IS NOT NULL AND uid <> ''`

// duplicateUID is one uid held by more than one node.
type duplicateUID struct {
	uid string
	ids []string
}

// uidQuerier is what duplicateNodeUIDs reads through: the database or an open transaction.
type uidQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// duplicateNodeUIDs returns every non-empty uid held by two or more nodes,
// soft-deleted ones included, with the node ids that hold it.
func (s *Store) duplicateNodeUIDs(ctx context.Context, q uidQuerier) ([]duplicateUID, error) {
	// Each shared uid with its holders, in a stable order.
	rows, err := q.QueryContext(ctx, `
		SELECT uid, id FROM nodes
		WHERE uid IN (SELECT uid FROM nodes WHERE uid IS NOT NULL AND uid <> ''
		              GROUP BY uid HAVING COUNT(*) > 1)
		ORDER BY uid, id`)
	if err != nil {
		return nil, fmt.Errorf("find duplicate node uids: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close duplicate uid rows", "error", closeErr)
		}
	}()
	var out []duplicateUID
	for rows.Next() {
		var uid, id string
		if err := rows.Scan(&uid, &id); err != nil {
			return nil, fmt.Errorf("find duplicate node uids: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].uid == uid {
			out[n-1].ids = append(out[n-1].ids, id)
		} else {
			out = append(out, duplicateUID{uid: uid, ids: []string{id}})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find duplicate node uids: %w", err)
	}
	return out, nil
}

// ensureUniqueUIDIndex makes idx_nodes_uid UNIQUE (MTIX-95.31.8). Idempotent:
// a store that already has the unique index is not touched.
//
// A store written before the index can already hold two nodes with one uid.
// The safe choice is to refuse nothing and change nothing: the duplicates are
// logged (duplicate_node_uids) and kept, the old non-unique index is left in
// place so lookups still work, and Verify (mtix verify / doctor) names each
// uid, its nodes and the recovery. No row is dropped and no uid is rewritten
// automatically, because which node keeps the uid is the owner's call (it may
// be on a sync hub). Every open retries, so the index appears on the first
// open after the duplicates are gone.
//
// The swap runs in ONE write transaction behind the free-space pre-flight, the
// state re-read inside it, so a failed CREATE rolls the DROP back and two
// opens never both swap. Like the uid backfill, a failure is logged and the
// store still opens (for example on a full disk, so a backup can be taken);
// the next open retries.
func (s *Store) ensureUniqueUIDIndex(ctx context.Context) error {
	return s.ensureUniqueUIDIndexWith(ctx, uidIndexDDL)
}

// ensureUniqueUIDIndexWith is ensureUniqueUIDIndex with the CREATE statement
// given, so a test can make it fail.
func (s *Store) ensureUniqueUIDIndexWith(ctx context.Context, createDDL string) error {
	err := s.WithTx(ctx, func(tx *sql.Tx) error { return s.swapUIDIndex(ctx, tx, createDDL) })
	if err != nil {
		s.logger.Warn("unique_uid_index_failed", "event", "unique_uid_index_failed",
			"error", fmt.Errorf("make idx_nodes_uid unique: %w", err),
			"note", "the store opens with its current uid index; the next open retries")
	}
	return nil
}

// swapUIDIndex does the work of ensureUniqueUIDIndexWith inside tx.
func (s *Store) swapUIDIndex(ctx context.Context, tx *sql.Tx, createDDL string) error {
	var state string
	// The index's current kind: unique, non-unique, or absent.
	err := tx.QueryRowContext(ctx, `
		SELECT CASE WHEN "unique" = 1 THEN 'unique' ELSE 'plain' END
		FROM pragma_index_list('nodes') WHERE name = 'idx_nodes_uid'`).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read idx_nodes_uid: %w", err)
	}
	if state == "unique" {
		return nil
	}
	dups, err := s.duplicateNodeUIDs(ctx, tx)
	if err != nil {
		return err
	}
	if len(dups) > 0 {
		s.logger.Error("duplicate_node_uids", "event", "duplicate_node_uids",
			"count", len(dups), "detail", duplicateUIDMessage(dups),
			"note", "unique uid index not created; rows kept; run mtix verify for the recovery")
		if state == "" {
			_, err = tx.ExecContext(ctx, plainUIDIndexDDL)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_nodes_uid`); err != nil {
		return fmt.Errorf("replace non-unique idx_nodes_uid: %w", err)
	}
	if _, err := tx.ExecContext(ctx, createDDL); err != nil {
		return fmt.Errorf("create unique idx_nodes_uid: %w", err)
	}
	return nil
}

// plainUIDIndexDDL keeps uid lookups indexed while duplicates remain.
const plainUIDIndexDDL = `CREATE INDEX IF NOT EXISTS idx_nodes_uid ON nodes(uid) WHERE uid IS NOT NULL AND uid <> ''`

// duplicateUIDMessage names each shared uid, its nodes and the recovery.
func duplicateUIDMessage(dups []duplicateUID) string {
	var b strings.Builder
	b.WriteString("nodes share a uid:")
	for _, d := range dups {
		fmt.Fprintf(&b, " uid %s is held by %s;", d.uid, strings.Join(d.ids, ", "))
	}
	b.WriteString(" recovery: back up the database (mtix backup .mtix/data/backups/pre-uid-fix.db), keep the uid on the original task, and for every other holder run" +
		" UPDATE nodes SET uid = '' WHERE id = '<id>' against .mtix/data/mtix.db with no mtix running, then open mtix again:" +
		" it gives that node a fresh uid and creates the unique index." +
		" If this project syncs (mtix sync status shows a project_prefix other than - or" +
		" a pushed or applied count above 0, MTIX_SYNC_DSN is set, or .mtix/secrets exists; when in doubt it does), do not do this alone:" +
		" a task given a new uid has no create event any peer holds, and events already queued under the shared uid are applied to the other holder." +
		" No mtix command repairs a synced project in this version (MTIX-95.31.8.1): hand it to a human, who first runs mtix sync push until mtix sync status shows pending 0.")
	return b.String()
}

// verifyUIDUnique records whether every non-empty uid names one node
// (MTIX-95.31.8), with the duplicates and the recovery when not.
func (s *Store) verifyUIDUnique(ctx context.Context, result *VerifyResult) error {
	dups, err := s.duplicateNodeUIDs(ctx, s.writeDB)
	if err != nil {
		return err
	}
	result.UIDUniqueOK = len(dups) == 0
	if len(dups) > 0 {
		result.Errors = append(result.Errors, "uid_unique: "+duplicateUIDMessage(dups))
	}
	return nil
}

// DuplicateNodeUIDsReport returns "" when every non-empty uid names one node,
// and otherwise the text naming each shared uid, its nodes and the exact
// recovery, which `mtix sync doctor` prints (MTIX-95.31.8).
func (s *Store) DuplicateNodeUIDsReport(ctx context.Context) (string, error) {
	dups, err := s.duplicateNodeUIDs(ctx, s.writeDB)
	if err != nil || len(dups) == 0 {
		return "", err
	}
	return duplicateUIDMessage(dups), nil
}
