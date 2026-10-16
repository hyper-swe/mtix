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

// StaleWakeClaims finds expired claims independently of the scan cursor (ADR-007 §6.3).
// The exact lease boundary is reclaimable; terminal send errors are never returned.
func (s *Store) StaleWakeClaims(ctx context.Context, lease time.Duration) (out []model.WakeAttempt, err error) {
	if lease <= 0 {
		return nil, fmt.Errorf("invalid wake claim lease: %w", model.ErrInvalidInput)
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT agent_id,host_id,uid,event_seq,epoch,attempt_token,claimed_at
		FROM wake_ledger WHERE outcome=? ORDER BY event_seq,agent_id,host_id,uid,epoch`, model.WakeClaimed)
	if err != nil {
		return nil, fmt.Errorf("read stale wake claims: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close wake claim rows: %w", closeErr))
		}
	}()
	cutoff := s.clock().UTC().Add(-lease)
	for rows.Next() {
		attempt, scanErr := scanWakeAttempt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if !attempt.ClaimedAt.After(cutoff) {
			out = append(out, attempt)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale wake claims: %w", err)
	}
	return out, nil
}

func scanWakeAttempt(rows *sql.Rows) (model.WakeAttempt, error) {
	var attempt model.WakeAttempt
	var claimed string
	k := &attempt.Key
	if err := rows.Scan(&k.AgentID, &k.HostID, &k.UID, &k.EventSeq, &k.Epoch, &attempt.Token, &claimed); err != nil {
		return attempt, fmt.Errorf("scan wake claim: %w", err)
	}
	when, err := time.Parse(time.RFC3339Nano, claimed)
	if err != nil {
		return attempt, fmt.Errorf("parse wake claim time: %w", err)
	}
	attempt.ClaimedAt = when.UTC()
	return attempt, nil
}
