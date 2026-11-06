// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// discardMissingHookClaim closes only an existing expired claim whose event
// is still absent. It never inserts or leaves a terminal row above the tail.
// The absence check and deletion share the writer transaction with replace.
func (s *Store) discardMissingHookClaim(ctx context.Context, hook string, seq int64, lease time.Duration) (bool, error) {
	var removed bool
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM hook_dispatch_ledger
   WHERE hook_name=? AND event_seq=? AND outcome=? AND fired_at<?
   AND NOT EXISTS(SELECT 1 FROM sync_events WHERE rowid=?)`,
			hook, seq, OutcomeClaimed, s.clock().UTC().Add(-lease).Format(time.RFC3339), seq)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		removed = count > 0
		return err
	})
	if err != nil {
		return false, fmt.Errorf("discard missing hook claim (%s, %d): %w", hook, seq, err)
	}
	return removed, nil
}
