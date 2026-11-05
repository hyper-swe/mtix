// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// salvageInboxKeys reads the covering identity index without touching damaged
// table leaves. If the index is damaged, the table walk is the fallback. Rowids
// are sorted so duplicate identities retain their earliest readable copy (FR-19.1).
func salvageInboxKeys(ctx context.Context, db *sql.DB) ([]int64, error) {
	keys, err := readInboxKeys(ctx, db, `SELECT rowid FROM sync_events ORDER BY event_id`)
	if err == nil {
		slices.Sort(keys)
		return slices.Compact(keys), nil
	}
	fallback, tableErr := readInboxKeys(ctx, db, `SELECT rowid FROM sync_events NOT INDEXED ORDER BY rowid`)
	keys = append(keys, fallback...)
	slices.Sort(keys)
	// A complete table walk covers every row even when the identity index failed.
	if tableErr == nil {
		return slices.Compact(keys), nil
	}
	return slices.Compact(keys), errors.Join(err, tableErr)
}

// readInboxKeys keeps the readable part of either key walk (FR-19.1).
func readInboxKeys(ctx context.Context, db *sql.DB, query string) ([]int64, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			return keys, err
		}
		keys = append(keys, seq)
	}
	return keys, errors.Join(rows.Err(), rows.Close())
}
