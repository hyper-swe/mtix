// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// wakeEventMatches validates a candidate in the same writer transaction as its
// mutation. Production inbox sources supply EventID; an empty value retains the
// legacy sequence-only store API. Missing or replaced events are quiet misses.
func wakeEventMatches(ctx context.Context, tx *sql.Tx, key model.WakeKey) (bool, error) {
	if key.EventID == "" {
		return true, nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT event_id FROM sync_events WHERE rowid=?`, key.EventSeq).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read wake event identity at sequence %d: %w", key.EventSeq, err)
	}
	return id == key.EventID, nil
}
