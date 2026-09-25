// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ResolveUIDByDisplayPath returns a node's durable UID given its
// dot-path id (ADR-003 §5). Returns model.ErrNotFound if no live node
// has that id.
func (s *Store) ResolveUIDByDisplayPath(ctx context.Context, displayPath string) (string, error) {
	var uid sql.NullString
	err := s.readDB.QueryRowContext(ctx,
		`SELECT uid FROM nodes WHERE id = ? AND deleted_at IS NULL`, displayPath).Scan(&uid)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("node %s: %w", displayPath, model.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("resolve uid for %s: %w", displayPath, err)
	}
	return uid.String, nil
}

// ResolveDisplayPathByUID returns a node's current dot-path id given its
// durable UID (ADR-003 §5). This is the resolution external references
// rely on so they survive a renumber. Returns model.ErrNotFound if no
// live node carries that uid.
func (s *Store) ResolveDisplayPathByUID(ctx context.Context, uid string) (string, error) {
	if uid == "" {
		return "", fmt.Errorf("empty uid: %w", model.ErrNotFound)
	}
	var id string
	err := s.readDB.QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE uid = ? AND deleted_at IS NULL`, uid).Scan(&id)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("uid %s: %w", uid, model.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("resolve display path for uid %s: %w", uid, err)
	}
	return id, nil
}

// BackfillUIDs assigns a UID to every node missing one (ADR-003 §7 Phase
// 0). Deterministic and replica-consistent: uid := the node's create_node
// event id, read from the local event log, so the same node gets the same
// uid on every machine. Nodes with no recoverable create event (pre-sync
// or imported data) get a locally-minted backfill uid (model.NewBackfillUID,
// MTIX-95.31.9) — safe because such data was never shared — and are
// logged. Idempotent: only fills empty uids.
func (s *Store) BackfillUIDs(ctx context.Context) error {
	// Step 1: deterministic fill from each node's create_node event.
	if _, err := s.writeDB.ExecContext(ctx, `
		UPDATE nodes
		   SET uid = (
		     SELECT e.event_id FROM sync_events e
		     WHERE e.node_id = nodes.id AND e.op_type = 'create_node'
		     ORDER BY e.lamport_clock ASC LIMIT 1)
		 WHERE (uid IS NULL OR uid = '')
		   AND EXISTS (
		     SELECT 1 FROM sync_events e2
		     WHERE e2.node_id = nodes.id AND e2.op_type = 'create_node')`,
	); err != nil {
		return fmt.Errorf("backfill uids from create events: %w", err)
	}

	// Step 2: local-mint for nodes still missing a uid (no create event).
	minted, err := s.mintMissingUIDs(ctx)
	if err != nil {
		return err
	}
	if minted > 0 {
		s.logger.Warn("backfill_uid_local_mint",
			"event", "backfill_uid_local_mint", "count", minted,
			"note", "nodes had no recoverable create event; locally-minted uids assigned (safe: never shared)")
	}
	return nil
}

// backfillUIDsOnOpen runs when the store opens (MTIX-95.31.9): the
// deterministic backfill of a pre-v3 database (backfillUIDsPreV3), then a
// backfill uid for every node still without one (NULL or empty), such as a
// node an older mtix imported from a board without uids, with the count
// logged. Idempotent: a store whose nodes all carry a uid is not written.
// A failed backfill is logged and retried at the next open, never fatal:
// the store must still open, for example on a full disk (MTIX-26), and a
// node without a uid is only compared by title until then.
func (s *Store) backfillUIDsOnOpen(ctx context.Context, existingVersion int) error {
	if err := s.backfillUIDsPreV3(ctx, existingVersion); err != nil {
		return err
	}
	minted, err := s.mintMissingUIDs(ctx)
	switch {
	case err != nil:
		s.logger.Warn("backfill_uid_on_open_failed", "event", "backfill_uid_on_open_failed",
			"error", fmt.Errorf("backfill uids on open: %w", err))
	case minted > 0:
		s.logger.Info("backfill_uid_on_open", "event", "backfill_uid_on_open", "count", minted)
	}
	return nil
}

// backfillUIDsPreV3 runs the deterministic UID backfill for a pre-v3
// database (MTIX-30.1 / ADR-003 §7 Phase 0). No-op on fresh DBs and v3+.
func (s *Store) backfillUIDsPreV3(ctx context.Context, existingVersion int) error {
	if existingVersion == 0 || existingVersion >= 3 {
		return nil
	}
	if err := s.BackfillUIDs(ctx); err != nil {
		return fmt.Errorf("migrate v2 -> v3 (backfill uids): %w", err)
	}
	s.logger.Info("schema_migrated",
		"event", "schema_migrated", "from_version", existingVersion, "to_version", 3,
		"added", "nodes.uid")
	return nil
}

// mintMissingUIDs gives every node without a uid (NULL or empty), live or
// soft-deleted, a backfill uid in one transaction, and returns how many
// it gave (MTIX-95.31.9). A node that got a uid meanwhile keeps it.
func (s *Store) mintMissingUIDs(ctx context.Context) (int, error) {
	ids, err := s.nodesMissingUID(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin the uid backfill: %w", err)
	}
	for _, id := range ids {
		uid, mintErr := model.NewBackfillUID()
		if mintErr != nil {
			return 0, errors.Join(fmt.Errorf("mint uid for %s: %w", id, mintErr), tx.Rollback())
		}
		// Give the node its uid, only while it still has none.
		if _, execErr := tx.ExecContext(ctx,
			`UPDATE nodes SET uid = ? WHERE id = ? AND COALESCE(uid, '') = ''`, uid, id); execErr != nil {
			return 0, errors.Join(fmt.Errorf("set local uid for %s: %w", id, execErr), tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit the uid backfill: %w", err)
	}
	return len(ids), nil
}

// uidForInsert returns the uid an import writes for a node it inserts
// (MTIX-95.31.9): the node's own, or, for a node without one (a board
// written before uids were shared), a new backfill uid, so every node in
// the store, and every board it writes, carries a uid.
func uidForInsert(uid string) (string, error) {
	if uid != "" {
		return uid, nil
	}
	minted, err := model.NewBackfillUID()
	if err != nil {
		return "", fmt.Errorf("give an imported node a uid: %w", err)
	}
	return minted, nil
}

// nodesMissingUID returns the ids of live nodes whose uid is still empty.
func (s *Store) nodesMissingUID(ctx context.Context) ([]string, error) {
	rows, err := s.writeDB.QueryContext(ctx,
		`SELECT id FROM nodes WHERE uid IS NULL OR uid = ''`)
	if err != nil {
		return nil, fmt.Errorf("scan nodes missing uid: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("scan node id: %w", scanErr)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
