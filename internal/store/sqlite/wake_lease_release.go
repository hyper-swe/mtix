// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Release checks durable inbox eligibility before retaining or relinquishing ownership.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ReleaseWakeLease releases or rearms the same owner without an ownership gap
// (FR-20). Eligibility and the new mark are checked in the write transaction.
func (s *Store) ReleaseWakeLease(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration, abandon bool) (ReleaseResult, error) {
	if err := validateWakeLease(key, holder, ttl); err != nil {
		return ReleaseResult{}, err
	}
	if holder == "" {
		return ReleaseResult{}, fmt.Errorf("release wake lease holder required: %w", model.ErrInvalidInput)
	}
	var result ReleaseResult
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		row, err := readWakeLease(ctx, tx, key)
		if errors.Is(err, sql.ErrNoRows) || err == nil && row.Holder != holder {
			return fmt.Errorf("not the holder of this wake lease: %w", model.ErrAlreadyClaimed)
		}
		if err != nil {
			return err
		}
		pending, above, err := wakePending(ctx, tx, key.AgentID, row.HighWater)
		if err != nil {
			return err
		}
		result = ReleaseResult{Outcome: "released", Pending: pending, HighWater: row.HighWater}
		if abandon {
			result.Outcome = "abandoned"
		}
		if !abandon && pending > 0 && (row.dirty || above > 0) {
			result, err = rearmWakeLease(ctx, tx, key, result, s.clock().UTC().Add(ttl))
			return err
		}
		return deleteWakeLease(ctx, tx, key)
	})
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("release wake lease: %w", err)
	}
	return result, nil
}

func wakePending(ctx context.Context, tx *sql.Tx, agent string, mark int64) (pending, above int, err error) {
	rows, err := queryInboxEligible(ctx, tx, agent)
	if err != nil {
		return 0, 0, fmt.Errorf("wake lease eligible inbox: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close wake lease inbox: %w", closeErr))
		}
	}()
	for rows.Next() {
		var seq int64
		var event, node, author, payload, created string
		if err = rows.Scan(&seq, &event, &node, &author, &payload, &created); err != nil {
			return 0, 0, fmt.Errorf("scan wake lease inbox: %w", err)
		}
		pending++
		if seq > mark {
			above++
		}
	}
	if err = rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate wake lease inbox: %w", err)
	}
	return pending, above, nil
}

func deleteWakeLease(ctx context.Context, tx *sql.Tx, key WakeLeaseKey) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM wake_leases WHERE agent_id=? AND host_id=? AND uid=?`, key.AgentID, key.HostID, key.UID)
	if err != nil {
		return fmt.Errorf("delete wake lease: %w", err)
	}
	return nil
}

func rearmWakeLease(ctx context.Context, tx *sql.Tx, key WakeLeaseKey, result ReleaseResult, expires time.Time) (ReleaseResult, error) {
	mark, err := journalTail(ctx, tx)
	if err != nil {
		return ReleaseResult{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE wake_leases SET high_water=?,dirty=0,expires_at=?
		WHERE agent_id=? AND host_id=? AND uid=?`, mark, expires.Format(time.RFC3339Nano), key.AgentID, key.HostID, key.UID)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("rearm wake lease: %w", err)
	}
	result.Outcome, result.HighWater, result.ExpiresAt = "relaunch", mark, &expires
	return result, nil
}
