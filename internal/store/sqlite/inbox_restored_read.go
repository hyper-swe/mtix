// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// SyncEventReader supplies read-only queries for replication counts (FR-19.1).
type SyncEventReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// restoredInboxQuery selects between two complete constant SQL statements.
// Old read-only stores have no provenance table and cannot migrate (FR-19.1).
func restoredInboxQuery(ctx context.Context, q SyncEventReader, legacy, current string) (string, error) {
	var present bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='inbox_restored_events')`).Scan(&present); err != nil {
		return "", fmt.Errorf("inspect restored inbox provenance: %w", err)
	}
	if !present {
		return legacy, nil
	}
	return current, nil
}

// CountReplicationEvents excludes local mail from replication state on every reader
// path, including pre-migration read-only stores (FR-19.1, MTIX-112.2.1).
// An empty status counts every real event; otherwise only that status is counted.
func CountReplicationEvents(ctx context.Context, q SyncEventReader, status string) (int, error) {
	query, err := restoredInboxQuery(ctx, q,
		`SELECT COUNT(*) FROM sync_events e WHERE (?='' OR e.sync_status=?)`,
		`SELECT COUNT(*) FROM sync_events e WHERE NOT EXISTS (SELECT 1 FROM inbox_restored_events r WHERE r.event_id=e.event_id) AND (?='' OR e.sync_status=?)`)
	if err != nil {
		return 0, err
	}
	var count int
	err = q.QueryRowContext(ctx, query, status, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count replication events: %w", err)
	}
	return count, nil
}

// CountSyncEventsByStatus supplies the same replication-only count to status (FR-18).
func (s *Store) CountSyncEventsByStatus(ctx context.Context, status string) (int, error) {
	return CountReplicationEvents(ctx, s.readDB, status)
}
