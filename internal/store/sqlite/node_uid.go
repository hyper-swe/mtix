// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
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
// or imported data) get a locally-minted UID — safe because such data was
// never shared — and are logged. Idempotent: only fills empty uids.
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
	orphans, err := s.nodesMissingUID(ctx)
	if err != nil {
		return err
	}
	for _, id := range orphans {
		uid, mintErr := clock.NewEventID()
		if mintErr != nil {
			return fmt.Errorf("mint uid for %s: %w", id, mintErr)
		}
		if _, execErr := s.writeDB.ExecContext(ctx,
			`UPDATE nodes SET uid = ? WHERE id = ?`, uid, id); execErr != nil {
			return fmt.Errorf("set local uid for %s: %w", id, execErr)
		}
	}
	if len(orphans) > 0 {
		s.logger.Warn("backfill_uid_local_mint",
			"event", "backfill_uid_local_mint", "count", len(orphans),
			"note", "nodes had no recoverable create event; locally-minted uids assigned (safe: never shared)")
	}
	return nil
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
