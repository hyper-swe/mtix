// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// RecordWakeOutcome accepts only the current attempt's terminal outcome (ADR-007 §6.3).
// Stale completions are durably recorded without overwriting the current claim.
func (s *Store) RecordWakeOutcome(ctx context.Context, attempt model.WakeAttempt, outcome model.WakeOutcome) error {
	if err := validateWakeClaimInput(attempt.Key, time.Minute); err != nil {
		return err
	}
	if attempt.Token == "" || (outcome != model.WakeDelivered && outcome != model.WakeError) {
		return fmt.Errorf("invalid wake terminal outcome or fence: %w", model.ErrInvalidInput)
	}
	refused := false
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		current, previous, found, err := readWakeAttempt(ctx, tx, attempt.Key)
		if err != nil {
			return err
		}
		if found && current.Token == attempt.Token {
			if previous == outcome {
				return nil
			}
			if previous == model.WakeClaimed {
				return writeWakeOutcome(ctx, tx, attempt, outcome)
			}
		}
		refused = true
		return s.recordWakeRejection(ctx, tx, attempt, outcome)
	})
	if err != nil {
		return fmt.Errorf("record wake outcome: %w", err)
	}
	if refused {
		return fmt.Errorf("wake attempt is no longer current: %w", model.ErrConflict)
	}
	return nil
}

func writeWakeOutcome(ctx context.Context, tx *sql.Tx, attempt model.WakeAttempt, outcome model.WakeOutcome) error {
	k := attempt.Key
	result, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND attempt_token=? AND outcome=?`,
		outcome, k.AgentID, k.HostID, k.UID, k.EventSeq, k.Epoch, attempt.Token, model.WakeClaimed)
	return checkWakeWrite(result, err, "finalize wake attempt")
}

func (s *Store) recordWakeRejection(ctx context.Context, tx *sql.Tx, attempt model.WakeAttempt, outcome model.WakeOutcome) error {
	k := attempt.Key
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO wake_outcome_rejections
		(agent_id,host_id,uid,event_seq,epoch,attempt_token,outcome,rejected_at) VALUES(?,?,?,?,?,?,?,?)`,
		k.AgentID, k.HostID, k.UID, k.EventSeq, k.Epoch, attempt.Token, outcome, s.clock().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record rejected wake completion: %w", err)
	}
	return nil
}

// WakeLedgerStats reports retained row counts without pruning or writing state.
func (s *Store) WakeLedgerStats(ctx context.Context) (model.WakeLedgerStats, error) {
	var stats model.WakeLedgerStats
	err := s.readDB.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(outcome=?),0),
		(SELECT count(*) FROM wake_outcome_rejections) FROM wake_ledger`, model.WakeClaimed).
		Scan(&stats.Rows, &stats.Claimed, &stats.Rejected)
	if err != nil {
		return stats, fmt.Errorf("read retained wake ledger counts: %w", err)
	}
	return stats, nil
}
