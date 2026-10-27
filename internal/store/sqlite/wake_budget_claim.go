// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
)

// ClaimWakeBudget atomically fences a claim and its monotonic local counter.
// Admission runs before either write; it must not send or reenter this store.
// Bookkeeping never invokes mutation callbacks while the operator lock is held.
func (s *Store) ClaimWakeBudget(ctx context.Context, key model.WakeKey, lease time.Duration,
	admit func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	if err := validateWakeClaimInput(key, lease); err != nil {
		return model.WakeBudgetResult{}, err
	}
	if admit == nil {
		return model.WakeBudgetResult{}, fmt.Errorf("wake admission is required: %w", model.ErrInvalidInput)
	}
	var result model.WakeBudgetResult
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		proposal, err := s.wakeBudgetProposal(ctx, tx, key, lease)
		if err != nil {
			return err
		}
		decision, err := admit(proposal)
		if err != nil {
			return fmt.Errorf("admit wake claim: %w", err)
		}
		result, err = applyWakeBudgetDecision(ctx, tx, key, proposal, decision)
		return err
	}, false)
	if err != nil {
		return model.WakeBudgetResult{}, fmt.Errorf("claim accounted wake: %w", err)
	}
	return result, nil
}

func (s *Store) wakeBudgetProposal(ctx context.Context, tx *sql.Tx, key model.WakeKey, lease time.Duration) (model.WakeBudgetProposal, error) {
	var p model.WakeBudgetProposal
	var err error
	if p.Snapshot, err = s.readWakeBudgetSnapshot(ctx, tx, key.Recipient()); err != nil {
		return p, err
	}
	previous, outcome, found, err := readWakeAttempt(ctx, tx, key)
	if err != nil {
		return p, err
	}
	now := s.clock().UTC()
	if found && (outcome != model.WakeClaimed || now.Sub(previous.ClaimedAt) < lease) {
		return p, nil
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return p, fmt.Errorf("generate accounted wake fence: %w", err)
	}
	if found && id.String() == previous.Token {
		return p, fmt.Errorf("accounted wake fence was not fresh: %w", model.ErrConflict)
	}
	p.Claimable = true
	p.Attempt = model.WakeAttempt{Key: key, Token: id.String(), ClaimedAt: now}
	return p, nil
}

func applyWakeBudgetDecision(ctx context.Context, tx *sql.Tx, key model.WakeKey,
	p model.WakeBudgetProposal, d model.WakeBudgetDecision,
) (model.WakeBudgetResult, error) {
	r := model.WakeBudgetResult{Snapshot: p.Snapshot, Held: p.Claimable && !d.Allow}
	if d.Floor < 0 {
		return r, fmt.Errorf("protected wake counter floor is negative: %w", model.ErrInvalidInput)
	}
	r.Snapshot.Total = max(r.Snapshot.Total, d.Floor)
	if p.Claimable && d.Allow {
		if r.Snapshot.Total == math.MaxInt64 {
			return r, fmt.Errorf("wake counter is exhausted: %w", model.ErrInvalidInput)
		}
		r.Snapshot.Total++
		if err := writeWakeAttempt(ctx, tx, p.Attempt); err != nil {
			return r, err
		}
		r.Attempt, r.Won = p.Attempt, true
	}
	if r.Snapshot.Total != p.Snapshot.Total {
		if err := writeWakeBudgetTotal(ctx, tx, key.Recipient(), r.Snapshot.Total); err != nil {
			return r, err
		}
	}
	return r, nil
}

func writeWakeBudgetTotal(ctx context.Context, tx *sql.Tx, key model.WakeRecipient, total int64) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO wake_budget_counters(agent_id,host_id,uid,total) VALUES(?,?,?,?)
		ON CONFLICT(agent_id,host_id,uid) DO UPDATE SET total=MAX(total,excluded.total)`,
		key.AgentID, key.HostID, key.UID, total)
	return checkWakeWrite(result, err, "persist wake counter")
}
