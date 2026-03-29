// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTx executes fn within a database transaction per CODING-STYLE.md §5.2.
// On success: commits the transaction.
// On error: rolls back the transaction and returns the error.
// On panic: rolls back the transaction and re-panics.
//
// All write operations MUST use this helper to ensure atomicity.
// Progress rollup MUST happen in the same transaction as the triggering change (FR-5.7).
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			// Rollback on panic, then re-panic.
			if rbErr := tx.Rollback(); rbErr != nil {
				s.logger.Error("rollback after panic failed",
					"panic", p,
					"rollback_error", rbErr,
				)
			}
			panic(p)
		}
	}()

	if err = fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			// Both the original error and rollback error matter.
			return fmt.Errorf("rollback failed (%v) after error: %w", rbErr, err)
		}
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}
