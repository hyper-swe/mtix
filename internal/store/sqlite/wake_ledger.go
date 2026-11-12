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
		var err error
		result, err = s.claimWakeTx(ctx, tx, key, lease, token)
		return err
	})
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("claim wake: %w", err)
	}
	return result, result.Token != "", nil
}

// claimWakeTx takes the claim inside the caller's transaction. A zero attempt means the
// claim was not won: the event is replaced, finished, in flight, or at the retry cap.
func (s *Store) claimWakeTx(ctx context.Context, tx *sql.Tx, key model.WakeKey, lease time.Duration, token func() (string, error)) (model.WakeAttempt, error) {
	matches, err := wakeEventMatches(ctx, tx, key)
	if err != nil || !matches {
		return model.WakeAttempt{}, err
	}
	previous, outcome, found, err := readWakeAttempt(ctx, tx, key)
	if err != nil {
		return model.WakeAttempt{}, err
	}
	now := s.clock().UTC()
	if found && (outcome != model.WakeClaimed || now.Sub(previous.ClaimedAt) < lease) {
		return model.WakeAttempt{}, nil
	}
	reclaim, err := s.admitReclaim(ctx, tx, key, found)
	if err != nil || (found && !reclaim) {
		return model.WakeAttempt{}, err
	}
	fence, err := token()
	if err != nil {
		return model.WakeAttempt{}, fmt.Errorf("generate wake attempt fence: %w", err)
	}
	if fence == "" || (found && fence == previous.Token) {
		return model.WakeAttempt{}, fmt.Errorf("wake attempt fence was not fresh: %w", model.ErrConflict)
	}
	result := model.WakeAttempt{Key: key, Token: fence, ClaimedAt: now, Reclaimed: found}
	return result, writeWakeAttempt(ctx, tx, result, false)
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

// writeWakeAttempt records a claim; charged says the wake budget admitted and
// charged it, which the inbox read uses to avoid charging the event again.
func writeWakeAttempt(ctx context.Context, tx *sql.Tx, attempt model.WakeAttempt, charged bool) error {
	k := attempt.Key
	// A reclaim records its retry in the reason column; a first claim leaves it empty.
	reason := ""
	if attempt.Reclaimed {
		reason = model.WakeReasonReclaimedAfterCrash
	}
	chargedFlag := 0
	if charged {
		chargedFlag = 1
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO wake_ledger
		(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome,attempt_token,charged,reason) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_id,host_id,uid,event_seq,epoch) DO UPDATE SET
		claimed_at=excluded.claimed_at,outcome=excluded.outcome,attempt_token=excluded.attempt_token,charged=excluded.charged,
		route='',reason=excluded.reason,outcome_at=''`,
		k.AgentID, k.HostID, k.UID, k.EventSeq, k.Epoch, attempt.ClaimedAt.Format(time.RFC3339Nano), model.WakeClaimed, attempt.Token, chargedFlag, reason)
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

// admitReclaim decides whether an expired unfinished claim may be taken over (MTIX-112.40.13).
// A row never claimed before is admitted. A row already reclaimed once is closed as
// undeliverable/reclaim-cap in this same transaction and refused: at most one redelivery.
func (s *Store) admitReclaim(ctx context.Context, tx *sql.Tx, key model.WakeKey, found bool) (bool, error) {
	if !found {
		return true, nil
	}
	var reason string
	// Read the retry marker of the expired unfinished claim.
	if err := tx.QueryRowContext(ctx, `SELECT reason FROM wake_ledger
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch).Scan(&reason); err != nil {
		return false, fmt.Errorf("read wake reclaim marker: %w", err)
	}
	if reason != model.WakeReasonReclaimedAfterCrash {
		return true, nil
	}
	// Close the event: a second expired claim means two claimants died unfinished.
	result, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?,reason=?,outcome_at=?
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND outcome=?`,
		model.WakeUndeliverable, model.WakeReasonReclaimCap, s.clock().UTC().Format(time.RFC3339Nano),
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch, model.WakeClaimed)
	return false, checkWakeWrite(result, err, "close wake at reclaim cap")
}
