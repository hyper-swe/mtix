// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

type wakeCursorRequest struct {
	host, uid string
	target    int64
	cutoff    time.Time
}

// AdvanceWakeScanCursor advances a shared optimization without consulting held registrations.
// Live claims clamp new progress; expired claims are found by StaleWakeClaims instead.
func (s *Store) AdvanceWakeScanCursor(ctx context.Context, host, uid string, target int64, lease time.Duration) error {
	if host == "" || uid == "" || target < 0 || lease <= 0 {
		return fmt.Errorf("invalid wake scan target or lease: %w", model.ErrInvalidInput)
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		request := wakeCursorRequest{host: host, uid: uid, target: target, cutoff: s.clock().UTC().Add(-lease)}
		floor, err := liveWakeScanTarget(ctx, tx, request)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO wake_scan_cursor(host_id,uid,cursor) VALUES(?,?,?)
			ON CONFLICT(host_id,uid) DO UPDATE SET cursor=max(wake_scan_cursor.cursor,excluded.cursor)`, host, uid, floor)
		if err != nil {
			return fmt.Errorf("write wake scan cursor: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("advance wake scan cursor: %w", err)
	}
	return nil
}

func liveWakeScanTarget(ctx context.Context, tx *sql.Tx, request wakeCursorRequest) (target int64, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,host_id,uid,event_seq,epoch,attempt_token,claimed_at
		FROM wake_ledger WHERE host_id=? AND uid=? AND outcome=?`, request.host, request.uid, model.WakeClaimed)
	if err != nil {
		return 0, fmt.Errorf("read live wake claims: %w", err)
	}
	defer closeWakeRows(rows, &err)
	target = request.target
	for rows.Next() {
		attempt, scanErr := scanWakeAttempt(rows)
		if scanErr != nil {
			return 0, scanErr
		}
		if attempt.Key.EventSeq <= 0 {
			return 0, fmt.Errorf("invalid retained wake event sequence: %w", model.ErrConflict)
		}
		if attempt.ClaimedAt.After(request.cutoff) && attempt.Key.EventSeq <= target {
			target = attempt.Key.EventSeq - 1
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate live wake claims: %w", err)
	}
	return target, nil
}

// WakeScanCursor reads the shared scan optimization; an absent row means zero.
func (s *Store) WakeScanCursor(ctx context.Context, host, uid string) (int64, error) {
	var cursor int64
	err := s.readDB.QueryRowContext(ctx, "SELECT cursor FROM wake_scan_cursor WHERE host_id=? AND uid=?", host, uid).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read wake scan cursor: %w", err)
	}
	return cursor, nil
}
