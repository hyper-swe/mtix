// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
)

// QueryRow executes a query on the read database pool that returns at most one row.
// This is a convenience method for simple read operations.
func (s *Store) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.readDB.QueryRowContext(ctx, query, args...)
}

// Query executes a query on the read database pool.
// The caller is responsible for closing the returned Rows.
func (s *Store) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.readDB.QueryContext(ctx, query, args...)
}

// WriteDB returns the write database pool.
// This is exposed for store operations that need direct write access
// outside of transactions (e.g., NextSequence which uses RETURNING).
func (s *Store) WriteDB() *sql.DB {
	return s.writeDB
}
