// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// initializeRegistrationStale adds the nullable stale_since marker to agent_registrations
// without changing schema_version (ADR-007 section 6.1, additive only). A 0.5.4 binary
// ignores the column: its INSERT and SELECT statements name their own columns, and its
// INSERT OR REPLACE of a row resets the marker to NULL, which only re-arms the scanner.
func (s *Store) initializeRegistrationStale(ctx context.Context) error {
	return s.withSchemaTx(ctx, func(tx *sql.Tx) error {
		var found int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM pragma_table_info(?) WHERE name=?", "agent_registrations", "stale_since").Scan(&found); err != nil {
			return fmt.Errorf("inspect registration stale marker: %w", err)
		}
		if found != 0 {
			return nil
		}
		// Constant additive DDL; existing rows read as NULL (not stale).
		if _, err := tx.ExecContext(ctx, "ALTER TABLE agent_registrations ADD COLUMN stale_since TEXT"); err != nil {
			return fmt.Errorf("add registration stale marker: %w", err)
		}
		return nil
	})
}

// MarkRegistrationStale records when the wake scanner first saw the registration's
// session dead (ADR-007 section 2.7 step 4). The update applies only to the row of the
// given epoch that is not yet marked, so a re-registered session (new epoch) is never
// marked by a stale scanner and the first-seen-dead time never moves. It returns the
// rows updated.
func (s *Store) MarkRegistrationStale(ctx context.Context, key RegistrationKey, epoch int64, since string) (int64, error) {
	var n int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Conditional marker write: same epoch, not already marked.
		res, err := tx.ExecContext(ctx,
			`UPDATE agent_registrations SET stale_since = ?
			  WHERE agent_id = ? AND host_id = ? AND uid = ? AND epoch = ? AND stale_since IS NULL`,
			since, key.AgentID, key.HostID, key.UID, epoch)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("mark registration stale %q: %w", key.AgentID, err)
	}
	return n, nil
}
