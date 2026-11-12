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

// ReopenStalledWake reopens a sent wake whose confirmation never came, so one redelivery
// can run through the single claim function (MTIX-112.40.12, FR-20, ADR-007 one claim per
// registration and event). The row must be sent (or legacy delivered), recorded more than window ago on the store clock. It becomes claimed again under a fresh fence in one transaction with a
// compare-and-set on the old fence, so concurrent reopeners have exactly one winner. The
// reopened claim carries the reclaimed-after-crash marker, so the retry cap is one: a row
// that already carries it is closed undeliverable/stall-unconfirmed and not reopened.
// A missing, finished-and-confirmed, in-flight or recent row returns false with no write.
func (s *Store) ReopenStalledWake(ctx context.Context, key model.WakeKey, window time.Duration) (model.WakeAttempt, bool, error) {
	if err := validateWakeClaimInput(key, time.Minute); err != nil {
		return model.WakeAttempt{}, false, err
	}
	if window <= 0 {
		return model.WakeAttempt{}, false, fmt.Errorf("stall window must be positive: %w", model.ErrInvalidInput)
	}
	var attempt model.WakeAttempt
	var reopened bool
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		attempt, reopened, err = s.reopenStalledTx(ctx, tx, key, window)
		return err
	})
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("reopen stalled wake: %w", err)
	}
	return attempt, reopened, nil
}

func (s *Store) reopenStalledTx(ctx context.Context, tx *sql.Tx, key model.WakeKey, window time.Duration) (model.WakeAttempt, bool, error) {
	matches, err := wakeEventMatches(ctx, tx, key)
	if err != nil || !matches {
		return model.WakeAttempt{}, false, err
	}
	var token, claimedAt, outcomeAt, reason string
	var outcome model.WakeOutcome
	// Read the row being judged: only the sent family can be a stalled, unconfirmed push.
	err = tx.QueryRowContext(ctx, `SELECT attempt_token,claimed_at,outcome_at,reason,outcome FROM wake_ledger
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch).Scan(&token, &claimedAt, &outcomeAt, &reason, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WakeAttempt{}, false, nil
	}
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("read stalled wake row: %w", err)
	}
	if outcome != model.WakeSent && outcome != model.WakeDelivered {
		return model.WakeAttempt{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, firstNonEmpty(outcomeAt, claimedAt))
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("parse stalled wake time: %w", err)
	}
	now := s.clock().UTC()
	if now.Sub(at) <= window { // T is measured on the store clock; exactly T is not yet stalled
		return model.WakeAttempt{}, false, nil
	}
	if reason == model.WakeReasonReclaimedAfterCrash {
		return model.WakeAttempt{}, false, s.closeStalledTx(ctx, tx, key, token, now)
	}
	fence, err := uuid.NewRandom()
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("generate wake attempt fence: %w", err)
	}
	// Compare-and-set on the old fence: a concurrent reopener or a recorded confirmation wins.
	result, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?,attempt_token=?,claimed_at=?,charged=0,route='',reason=?,outcome_at=''
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND attempt_token=? AND outcome IN (?,?)`,
		model.WakeClaimed, fence.String(), now.Format(time.RFC3339Nano), model.WakeReasonReclaimedAfterCrash,
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch, token, model.WakeSent, model.WakeDelivered)
	if werr := checkWakeWrite(result, err, "reopen stalled wake"); werr != nil {
		return model.WakeAttempt{}, false, werr
	}
	return model.WakeAttempt{Key: key, Token: fence.String(), ClaimedAt: now, Reclaimed: true}, true, nil
}

// closeStalledTx ends an event whose redelivery also stalled: reported, never retried again.
func (s *Store) closeStalledTx(ctx context.Context, tx *sql.Tx, key model.WakeKey, token string, now time.Time) error {
	// Close under the same fence read above, so a concurrent confirmation is not overwritten.
	result, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?,reason=?,outcome_at=?
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND attempt_token=? AND outcome IN (?,?)`,
		model.WakeUndeliverable, model.WakeReasonStallUnconfirmed, now.Format(time.RFC3339Nano),
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch, token, model.WakeSent, model.WakeDelivered)
	return checkWakeWrite(result, err, "close stalled wake at retry cap")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// WakeWaiterLive reports whether an unexpired wake lease exists for the registration: a hook
// waiter is armed and will wake the session when mail arrives. No lease row, or an expired
// one, means nobody is waiting (MTIX-112.40.12). It is a read-only query.
func (s *Store) WakeWaiterLive(ctx context.Context, key WakeLeaseKey) (bool, error) {
	if key.AgentID == "" || key.HostID == "" || key.UID == "" {
		return false, fmt.Errorf("wake waiter identity required: %w", model.ErrInvalidInput)
	}
	var expires string
	// Read the lease expiry for this registration; an unexpired lease is a live waiter.
	err := s.readDB.QueryRowContext(ctx, `SELECT expires_at FROM wake_leases WHERE agent_id=? AND host_id=? AND uid=?`,
		key.AgentID, key.HostID, key.UID).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read wake waiter lease: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return false, fmt.Errorf("parse wake waiter lease expiry: %w", err)
	}
	return at.After(s.clock().UTC()), nil
}
