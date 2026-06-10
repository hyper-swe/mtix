// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// WithTx executes fn within a database transaction per CODING-STYLE.md §5.2.
// On success: commits the transaction.
// On error: rolls back the transaction and returns the error.
// On panic: rolls back the transaction and re-panics.
//
// All write operations MUST use this helper to ensure atomicity.
// Progress rollup MUST happen in the same transaction as the triggering change (FR-5.7).
//
// Per NFR-2.8, every transaction is preceded by a free-space pre-flight
// (a commit can trigger a WAL autocheckpoint, and a checkpoint on a full
// disk is exactly the failure that tears the main file), and any fatal
// I/O error latches the store into fail-stop.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	if pfErr := s.preflightWrite(); pfErr != nil {
		return pfErr
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return s.classifyWriteError(wrapBusyError(fmt.Errorf("begin transaction: %w", err)))
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
			return s.classifyWriteError(fmt.Errorf("rollback failed (%v) after error: %w", rbErr, err))
		}
		return s.classifyWriteError(err)
	}

	if err = tx.Commit(); err != nil {
		return s.classifyWriteError(wrapBusyError(fmt.Errorf("commit transaction: %w", err)))
	}

	if s.onCommit != nil {
		s.onCommit()
	}

	return nil
}

// wrapBusyError checks if an error is a SQLite busy/locked error and wraps
// it with actionable guidance for agents and users.
func wrapBusyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY") {
		return fmt.Errorf("another mtix operation is in progress, retry in a moment: %w", err)
	}
	return err
}
