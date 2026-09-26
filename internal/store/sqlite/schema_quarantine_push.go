// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// widenQuarantineSQL rebuilds sync_quarantine with the source CHECK that
// accepts push (MTIX-95.12), keeping every row. Constant DDL (SQL rule 1a).
// SQLite cannot alter a CHECK constraint in place, so the table is copied.
const widenQuarantineSQL = `
CREATE TABLE sync_quarantine_widened (
    event_id     TEXT PRIMARY KEY,
    source       TEXT NOT NULL CHECK (source IN ('pull', 'sweep', 'push')),
    raw_event    TEXT NOT NULL,
    reason       TEXT NOT NULL,
    first_seen   TEXT NOT NULL,
    last_attempt TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 1,
    cli_version  TEXT NOT NULL DEFAULT ''
);
INSERT INTO sync_quarantine_widened
  (event_id, source, raw_event, reason, first_seen, last_attempt, attempts, cli_version)
SELECT event_id, source, raw_event, reason, first_seen, last_attempt, attempts, cli_version
FROM sync_quarantine;
DROP TABLE sync_quarantine;
ALTER TABLE sync_quarantine_widened RENAME TO sync_quarantine;
`

// createSchema runs schemaSQL (every CREATE ... IF NOT EXISTS and meta
// default) and then widenQuarantineSource.
func (s *Store) createSchema(ctx context.Context) error {
	if _, err := s.writeDB.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return s.widenQuarantineSource(ctx)
}

// widenQuarantineSource lets a sync_quarantine table created before push
// holds (MTIX-95.12) accept source push: CREATE TABLE IF NOT EXISTS leaves
// such a table with the CHECK that allows only pull and sweep, and every
// push hold would then fail. The table is rebuilt in one transaction with
// its rows kept. A table whose CHECK already names push is left alone, so
// the step runs at most once per store.
func (s *Store) widenQuarantineSource(ctx context.Context) error {
	var ddl string
	// The stored CREATE statement of the quarantine table.
	if err := s.writeDB.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'sync_quarantine'`,
	).Scan(&ddl); err != nil {
		return fmt.Errorf("read sync_quarantine schema: %w", err)
	}
	if strings.Contains(ddl, "'push'") {
		return nil
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("widen sync_quarantine: begin: %w", err)
	}
	// Copy the table under the widened CHECK, keeping every row.
	if _, err := tx.ExecContext(ctx, widenQuarantineSQL); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("widen sync_quarantine: %w (rollback: %w)", err, rbErr)
		}
		return fmt.Errorf("widen sync_quarantine: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("widen sync_quarantine: commit: %w", err)
	}
	s.logger.Info("schema_migrated", "event", "sync_quarantine_widened", "source_added", "push")
	return nil
}
