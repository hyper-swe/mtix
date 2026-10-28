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

// RecordWakeResult accepts the current attempt's terminal adapter result (ADR-007 §6.3,
// MTIX-112.40 design note section 4). It shares the fence and stale-completion rules of
// RecordWakeOutcome and also stores the route, reason and time of the outcome.
// confirmed is never accepted here: UpgradeWakeConfirmed owns that transition.
func (s *Store) RecordWakeResult(ctx context.Context, attempt model.WakeAttempt, r model.WakeResult) error {
	if err := validateWakeClaimInput(attempt.Key, time.Minute); err != nil {
		return err
	}
	if attempt.Token == "" || !r.Outcome.Terminal() || r.Outcome == model.WakeConfirmed ||
		(r.Outcome == model.WakeUndeliverable && r.Reason == "") {
		return fmt.Errorf("invalid wake terminal result or fence: %w", model.ErrInvalidInput)
	}
	refused := false
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		current, previous, found, err := readWakeAttempt(ctx, tx, attempt.Key)
		if err != nil {
			return err
		}
		if found && current.Token == attempt.Token {
			if previous == r.Outcome {
				return nil
			}
			if previous == model.WakeClaimed {
				return s.writeWakeResult(ctx, tx, attempt, r)
			}
		}
		refused = true
		return s.recordWakeRejection(ctx, tx, attempt, r.Outcome)
	})
	if err != nil {
		return fmt.Errorf("record wake result: %w", err)
	}
	if refused {
		return fmt.Errorf("wake attempt is no longer current: %w", model.ErrConflict)
	}
	return nil
}

func (s *Store) writeWakeResult(ctx context.Context, tx *sql.Tx, attempt model.WakeAttempt, r model.WakeResult) error {
	k := attempt.Key
	// Finalize only the still-claimed row that carries this attempt's fence.
	result, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?,route=?,reason=?,outcome_at=?
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND attempt_token=? AND outcome=?`,
		r.Outcome, r.Route, r.Reason, s.clock().UTC().Format(time.RFC3339Nano),
		k.AgentID, k.HostID, k.UID, k.EventSeq, k.Epoch, attempt.Token, model.WakeClaimed)
	return checkWakeWrite(result, err, "finalize wake result")
}

// UpgradeWakeConfirmed moves a sent (or legacy delivered) row to confirmed. It is
// fenced by the key, idempotent, never downgrades and never creates a claim: an
// unknown key is ErrNotFound and any other state is ErrInvalidTransition.
func (s *Store) UpgradeWakeConfirmed(ctx context.Context, key model.WakeKey) error {
	if err := validateWakeClaimInput(key, time.Minute); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		// Upgrade only from the sent family; every other state is left untouched.
		res, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?,outcome_at=?
			WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND outcome IN (?,?)`,
			model.WakeConfirmed, s.clock().UTC().Format(time.RFC3339Nano),
			key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch, model.WakeSent, model.WakeDelivered)
		n, err := wakeRowsChanged(res, err)
		if err != nil || n > 0 {
			return err
		}
		var current model.WakeOutcome
		err = tx.QueryRowContext(ctx, `SELECT outcome FROM wake_ledger
			WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
			key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch).Scan(&current)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("confirm wake: no ledger row: %w", model.ErrNotFound)
		case err != nil:
			return fmt.Errorf("read wake outcome: %w", err)
		case current == model.WakeConfirmed:
			return nil
		}
		return fmt.Errorf("confirm wake from %s: %w", current, model.ErrInvalidTransition)
	})
}

func wakeRowsChanged(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, fmt.Errorf("confirm wake: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("confirm wake rows: %w", err)
	}
	return n, nil
}

// ReadWakeResult returns the stored result with legacy names mapped at read time
// (delivered reads as sent, error as undeliverable with reason send-error). Rows are not rewritten.
func (s *Store) ReadWakeResult(ctx context.Context, key model.WakeKey) (model.WakeResult, bool, error) {
	var r model.WakeResult
	// Read-only lookup of one registration/event outcome with its route, reason and time.
	err := s.readDB.QueryRowContext(ctx, `SELECT outcome,route,reason,outcome_at FROM wake_ledger
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch).Scan(&r.Outcome, &r.Route, &r.Reason, &r.At)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WakeResult{}, false, nil
	}
	if err != nil {
		return model.WakeResult{}, false, fmt.Errorf("read wake result: %w", err)
	}
	if r.Outcome == model.WakeError && r.Reason == "" {
		r.Reason = "send-error"
	}
	r.Outcome = r.Outcome.Canonical()
	return r, true, nil
}
