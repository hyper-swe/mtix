// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
)

// ClaimWake serializes one registration/event claim and returns its attempt fence.
// Terminal rows are retained; the scan cursor never prevents a registration's claim.
func (s *Store) ClaimWake(ctx context.Context, key model.WakeKey, lease time.Duration) (model.WakeAttempt, bool, error) {
	return s.claimWake(ctx, key, lease, func() (string, error) {
		id, err := uuid.NewRandom()
		return id.String(), err
	})
}

func (s *Store) claimWake(ctx context.Context, key model.WakeKey, lease time.Duration, token func() (string, error)) (model.WakeAttempt, bool, error) {
	if err := validateWakeClaimInput(key, lease); err != nil {
		return model.WakeAttempt{}, false, err
	}
	var result model.WakeAttempt
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		previous, outcome, found, err := readWakeAttempt(ctx, tx, key)
		if err != nil {
			return err
		}
		now := s.clock().UTC()
		if found && (outcome != model.WakeClaimed || now.Sub(previous.ClaimedAt) < lease) {
			return nil
		}
		fence, err := token()
		if err != nil {
			return fmt.Errorf("generate wake attempt fence: %w", err)
		}
		if fence == "" || (found && fence == previous.Token) {
			return fmt.Errorf("wake attempt fence was not fresh: %w", model.ErrConflict)
		}
		result = model.WakeAttempt{Key: key, Token: fence, ClaimedAt: now}
		return writeWakeAttempt(ctx, tx, result)
	})
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("claim wake: %w", err)
	}
	return result, result.Token != "", nil
}

func validateWakeClaimInput(key model.WakeKey, lease time.Duration) error {
	if key.AgentID == "" || key.HostID == "" || key.UID == "" || key.EventSeq <= 0 || key.Epoch < 0 || lease <= 0 {
		return fmt.Errorf("invalid wake claim key or lease: %w", model.ErrInvalidInput)
	}
	return nil
}

func readWakeAttempt(ctx context.Context, tx *sql.Tx, key model.WakeKey) (model.WakeAttempt, model.WakeOutcome, bool, error) {
	attempt := model.WakeAttempt{Key: key}
	var claimed string
	var outcome model.WakeOutcome
	err := tx.QueryRowContext(ctx, `SELECT attempt_token,claimed_at,outcome FROM wake_ledger
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch).Scan(&attempt.Token, &claimed, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return attempt, outcome, false, nil
	}
	if err != nil {
		return attempt, outcome, false, fmt.Errorf("read wake attempt: %w", err)
	}
	if outcome == model.WakeClaimed {
		attempt.ClaimedAt, err = time.Parse(time.RFC3339Nano, claimed)
		if err != nil {
			return attempt, outcome, false, fmt.Errorf("parse wake claim time: %w", err)
		}
	}
	return attempt, outcome, true, nil
}

func writeWakeAttempt(ctx context.Context, tx *sql.Tx, attempt model.WakeAttempt) error {
	k := attempt.Key
	result, err := tx.ExecContext(ctx, `INSERT INTO wake_ledger
		(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome,attempt_token) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_id,host_id,uid,event_seq,epoch) DO UPDATE SET
		claimed_at=excluded.claimed_at,outcome=excluded.outcome,attempt_token=excluded.attempt_token`,
		k.AgentID, k.HostID, k.UID, k.EventSeq, k.Epoch, attempt.ClaimedAt.Format(time.RFC3339Nano), model.WakeClaimed, attempt.Token)
	return checkWakeWrite(result, err, "write wake attempt")
}

func checkWakeWrite(result sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s row count: %w", action, err)
	}
	if count != 1 {
		return fmt.Errorf("%s did not persist exactly one row: %w", action, model.ErrConflict)
	}
	return nil
}
