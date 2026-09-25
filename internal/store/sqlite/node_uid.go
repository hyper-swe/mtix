// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
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
// soft-deleted, a backfill uid, and returns how many it gave, counted from
// the rows it changed (MTIX-95.31.9). A store whose nodes all carry a uid
// is only read. Otherwise the backfill is a write like any other, through
// WithTx: the NFR-2.8 free-space pre-flight may refuse it, and a fatal
// storage error latches fail-stop. The nodes are found inside the
// transaction, and each is written only while it still has no uid.
func (s *Store) mintMissingUIDs(ctx context.Context) (int, error) {
	var missing bool
	// Does any node, soft-deleted ones included, lack a uid?
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM nodes WHERE uid IS NULL OR uid = '')`).Scan(&missing); err != nil {
		return 0, fmt.Errorf("look for nodes without a uid: %w", err)
	}
	if !missing {
		return 0, nil
	}
	minted := 0
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		ids, err := uidlessNodeIDs(ctx, tx)
		if err != nil {
			return err
		}
		minted, err = setBackfillUIDs(ctx, tx, ids)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("give nodes without a uid a backfill uid: %w", err)
	}
	return minted, nil
}

// uidlessNodeIDs returns, read through tx, the ids of the nodes without a
// uid (NULL or empty), soft-deleted ones included (MTIX-95.31.9).
func uidlessNodeIDs(ctx context.Context, tx *sql.Tx) (ids []string, err error) {
	// Every node without a uid.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM nodes WHERE uid IS NULL OR uid = ''`)
	if err != nil {
		return nil, fmt.Errorf("scan nodes missing uid: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the scan of nodes missing uid: %w", closeErr)
		}
	}()
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("scan node id: %w", scanErr)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan nodes missing uid: %w", err)
	}
	return ids, nil
}

// setBackfillUIDs gives each node of ids, inside tx, a backfill uid while
// it still has none (setBackfillUID), and returns how many it gave, counted
// from the rows it changed (MTIX-95.31.9).
func setBackfillUIDs(ctx context.Context, tx *sql.Tx, ids []string) (int, error) {
	set := 0
	for _, id := range ids {
		ok, err := setBackfillUID(ctx, tx, id)
		if err != nil {
			return set, err
		}
		if ok {
			set++
		}
	}
	return set, nil
}

// setBackfillUID gives node id, inside tx, a new backfill uid, only while it
// still has none, and reports whether it did (MTIX-95.31.9): a node another
// process gave a uid after the scan keeps that uid and is not counted.
func setBackfillUID(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	uid, err := model.NewBackfillUID()
	if err != nil {
		return false, fmt.Errorf("mint uid for %s: %w", id, err)
	}
	// Give the node its uid, only while it still has none.
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET uid = ? WHERE id = ? AND COALESCE(uid, '') = ''`, uid, id)
	if err != nil {
		return false, fmt.Errorf("set local uid for %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set local uid for %s: %w", id, err)
	}
	return n == 1, nil
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
