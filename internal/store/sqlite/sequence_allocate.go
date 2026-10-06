// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// sequenceQuery lets local creation allocate through its transaction (FR-2.7).
type sequenceQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// nextSequenceQuery retains the atomic increment and skip-once contract (MTIX-95.38).
// A transaction caller rolls back both increments if a later create write fails.
func nextSequenceQuery(ctx context.Context, query sequenceQuery, key string) (int, error) {
	var value int
	// Atomic upsert per FR-2.7; counters at the supported limit do not change.
	err := query.QueryRowContext(ctx,
		`INSERT INTO sequences (key, value) VALUES (?, 1)
   ON CONFLICT(key) DO UPDATE SET value = value + 1 WHERE value < ?
   RETURNING value`, key, maxSequence).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sequenceLimitError(key)
	}
	if err != nil {
		return 0, fmt.Errorf("next sequence for %s: %w", key, err)
	}
	return skipTakenSequenceQuery(ctx, query, key, value)
}

// skipTakenSequence retains the standalone counter operation for its existing callers.
func (s *Store) skipTakenSequence(ctx context.Context, key string, value int) (int, error) {
	next, err := skipTakenSequenceQuery(ctx, s.writeDB, key, value)
	return next, s.classifyWriteError(err)
}
