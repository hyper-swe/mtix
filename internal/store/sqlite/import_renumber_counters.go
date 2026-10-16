// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// counterSnapshot maps a sequence key to its stored value.
type counterSnapshot map[string]int

// snapshotCounters reads every sequence counter inside tx, before
// applyLocalRenumbers parks tasks at temporary numbers (MTIX-95.31.27).
func snapshotCounters(ctx context.Context, tx *sql.Tx) (counterSnapshot, error) {
	// Every counter row: key and stored value.
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM sequences`)
	if err != nil {
		return nil, fmt.Errorf("read sequence counters: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // a read-only cursor; its error is checked via rows.Err
	snap := counterSnapshot{}
	for rows.Next() {
		var key string
		var value int
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan sequence counter: %w", err)
		}
		snap[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sequence counters: %w", err)
	}
	return snap, nil
}

// restoreRenumberCounters sets every counter the renumber changed back to
// max(its value before the renumber, the highest seq under its parent now),
// inside tx (MTIX-95.31.27, FR-7.8.x). The temporary number a moved task is
// parked at (temporarySeqBase) raised a counter; without this it would stay
// there, since counters are never lowered, and every later id in the
// namespace would skip ahead. The counter keeps the final numbers, which
// advanceRenumberedSequences raised it to, and any value it held before.
func restoreRenumberCounters(ctx context.Context, tx *sql.Tx, before counterSnapshot) error {
	// The highest seq of the nodes under each project:parent key, deleted
	// rows included, as rebuildSequencesTx counts them.
	rows, err := tx.QueryContext(ctx,
		`SELECT project || ':' || COALESCE(parent_id, ''), MAX(seq)
		 FROM nodes GROUP BY project, COALESCE(parent_id, '')`)
	if err != nil {
		return fmt.Errorf("read the highest node numbers: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // a read-only cursor; its error is checked via rows.Err
	highest := counterSnapshot{}
	for rows.Next() {
		var key string
		var seq int
		if err := rows.Scan(&key, &seq); err != nil {
			return fmt.Errorf("scan the highest node number: %w", err)
		}
		highest[key] = seq
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the highest node numbers: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close the highest node numbers: %w", err)
	}
	return setRestoredCounters(ctx, tx, before, highest)
}

// setRestoredCounters writes max(before, highest) to each counter whose
// stored value differs from it.
func setRestoredCounters(ctx context.Context, tx *sql.Tx, before, highest counterSnapshot) error {
	now, err := snapshotCounters(ctx, tx)
	if err != nil {
		return err
	}
	for key, value := range now {
		want := max(before[key], highest[key])
		if value == want {
			continue
		}
		// Set the counter to the value it should hold after the renumber.
		if _, err := tx.ExecContext(ctx, `UPDATE sequences SET value = ? WHERE key = ?`, want, key); err != nil {
			return fmt.Errorf("restore sequence counter %s: %w", key, err)
		}
	}
	return nil
}
