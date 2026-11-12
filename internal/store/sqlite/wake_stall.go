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
// registration and event). It is the same admission transaction as ClaimWakeBudget: the
// proposal is built here, admit applies the budget and back-off policy, and only an allowed
// proposal is written. The row must be sent (or legacy delivered), recorded more than window
// ago on the store clock. It becomes claimed again under a fresh fence in one write
// transaction, so concurrent reopeners have exactly one winner. The reopened claim carries
// the reclaimed-after-crash marker, so the retry cap is one: a row that already carries it
// is closed undeliverable/stall-unconfirmed and not reopened. A missing, confirmed,
// in-flight or recent row is returned unclaimable with no write.
func (s *Store) ReopenStalledWake(ctx context.Context, key model.WakeKey, window time.Duration,
	admit func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	if window <= 0 {
		return model.WakeBudgetResult{}, fmt.Errorf("stall window must be positive: %w", model.ErrInvalidInput)
	}
	result, err := s.claimWithProposal(ctx, key, time.Minute, admit, func(tx *sql.Tx) (model.WakeBudgetProposal, error) {
		return s.stalledProposal(ctx, tx, key, window)
	})
	if err != nil {
		return model.WakeBudgetResult{}, fmt.Errorf("reopen stalled wake: %w", err)
	}
	return result, nil
}

// stalledProposal builds the proposal for a stalled sent row: claimable only when the row is
// a sent push older than window that has not been reopened before.
func (s *Store) stalledProposal(ctx context.Context, tx *sql.Tx, key model.WakeKey, window time.Duration) (model.WakeBudgetProposal, error) {
	p, err := s.baseWakeProposal(ctx, tx, key)
	if err != nil {
		return p, err
	}
	var token, claimedAt, outcomeAt, reason string
	var outcome model.WakeOutcome
	// Read the row being judged: only the sent family can be a stalled, unconfirmed push.
	err = tx.QueryRowContext(ctx, `SELECT attempt_token,claimed_at,outcome_at,reason,outcome FROM wake_ledger
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=?`,
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch).Scan(&token, &claimedAt, &outcomeAt, &reason, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("read stalled wake row: %w", err)
	}
	if outcome != model.WakeSent && outcome != model.WakeDelivered {
		return p, nil
	}
	if acked, aerr := eventAcknowledged(ctx, tx, key); aerr != nil {
		return p, aerr
	} else if acked {
		// Read between the candidate scan and this write: settle it confirmed, charge nothing.
		return p, s.confirmAckedTx(ctx, tx, key, token)
	}
	at, err := time.Parse(time.RFC3339Nano, firstNonEmpty(outcomeAt, claimedAt))
	if err != nil {
		return p, fmt.Errorf("parse stalled wake time: %w", err)
	}
	now := s.clock().UTC()
	if now.Sub(at) <= window { // T is measured on the store clock; exactly T is not yet stalled
		return p, nil
	}
	if reason == model.WakeReasonReclaimedAfterCrash {
		return p, s.closeStalledTx(ctx, tx, key, token, now)
	}
	fence, err := uuid.NewRandom()
	if err != nil {
		return p, fmt.Errorf("generate wake attempt fence: %w", err)
	}
	if p.Sender, err = wakeBudgetSender(ctx, tx, key); err != nil {
		return p, fmt.Errorf("read wake sender: %w", err)
	}
	p.Claimable = true
	p.Attempt = model.WakeAttempt{Key: key, Token: fence.String(), ClaimedAt: now, Reclaimed: true}
	return p, nil
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

// StalledWakeQuery selects stalled-turn candidates (MTIX-112.40.12). Limit bounds the page.
// An empty HostID or UID does not narrow the read. A non-empty Harness keeps only agents
// registered on that host and user with that harness (the hook route). AfterSeq and
// AfterAgent are a keyset cursor: only rows after (AfterSeq, AfterAgent) are returned, so
// a pass can page past rows the dispatcher gates refuse instead of re-reading them.
type StalledWakeQuery struct {
	HostID, UID, Harness string
	Limit                int
	AfterSeq             int64
	AfterAgent           string
}

// StalledWakeCandidates lists, oldest event first (event_seq, then agent) and at most
// q.Limit, the sent (or legacy delivered) wake rows of this host and operating-system user
// whose event is still unacknowledged and whose agent is on the requested harness: the
// events a stalled turn may have left unconfirmed (MTIX-112.40.12). It does not judge the
// window; ReopenStalledWake does that against the store clock. It is read-only, needs no
// timer, and rows of agents that cannot be recovered never occupy the window.
func (s *Store) StalledWakeCandidates(ctx context.Context, q StalledWakeQuery) ([]model.WakeKey, error) {
	if q.Limit <= 0 {
		return nil, fmt.Errorf("stalled candidate limit must be positive: %w", model.ErrInvalidInput)
	}
	// Sent and delivered rows (the hook waiter records delivered), restricted to registrations
	// of the hook harness when one is asked for. The event must not be covered by the agent's
	// cursor and must have no selective ack. The keyset cursor resumes after the last row a
	// pass examined.
	rows, err := s.readDB.QueryContext(ctx, `SELECT w.agent_id,w.host_id,w.uid,w.event_seq,w.epoch FROM wake_ledger w
		WHERE w.outcome IN (?,?) AND (?='' OR w.host_id=?) AND (?='' OR w.uid=?)
		  AND (?='' OR EXISTS (SELECT 1 FROM agent_registrations r
		        WHERE r.agent_id=w.agent_id AND r.host_id=w.host_id AND r.uid=w.uid AND r.harness=?))
		  AND (w.event_seq > ? OR (w.event_seq = ? AND w.agent_id > ?))
		  AND w.event_seq > COALESCE((SELECT cursor FROM agent_inbox_cursor WHERE agent_id=w.agent_id),0)
		  AND NOT EXISTS (SELECT 1 FROM agent_inbox_ack WHERE agent_id=w.agent_id AND event_seq=w.event_seq)
		ORDER BY w.event_seq ASC, w.agent_id ASC LIMIT ?`,
		model.WakeSent, model.WakeDelivered, q.HostID, q.HostID, q.UID, q.UID, q.Harness, q.Harness,
		q.AfterSeq, q.AfterSeq, q.AfterAgent, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("list stalled wake candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var keys []model.WakeKey
	for rows.Next() {
		var k model.WakeKey
		if err := rows.Scan(&k.AgentID, &k.HostID, &k.UID, &k.EventSeq, &k.Epoch); err != nil {
			return nil, fmt.Errorf("scan stalled wake candidate: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stalled wake candidates: %w", err)
	}
	return keys, nil
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

// eventAcknowledged reports whether the agent's cursor covers the event or a selective ack
// exists, read inside the reopen transaction so a race with a read is judged on committed
// state (MTIX-112.40.12).
func eventAcknowledged(ctx context.Context, tx *sql.Tx, key model.WakeKey) (bool, error) {
	var acked int
	// One row when the agent already read the event: its cursor is at or past it, or a selective ack exists.
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agent_inbox_cursor WHERE agent_id=? AND cursor>=?)
		OR EXISTS (SELECT 1 FROM agent_inbox_ack WHERE agent_id=? AND event_seq=?)`,
		key.AgentID, key.EventSeq, key.AgentID, key.EventSeq).Scan(&acked)
	if err != nil {
		return false, fmt.Errorf("read event acknowledgement: %w", err)
	}
	return acked == 1, nil
}

// confirmAckedTx settles a sent row whose event was read as confirmed, under the fence read
// by the caller, so it is neither reopened nor charged to the budget.
func (s *Store) confirmAckedTx(ctx context.Context, tx *sql.Tx, key model.WakeKey, token string) error {
	result, err := tx.ExecContext(ctx, `UPDATE wake_ledger SET outcome=?,outcome_at=?
		WHERE agent_id=? AND host_id=? AND uid=? AND event_seq=? AND epoch=? AND attempt_token=? AND outcome IN (?,?)`,
		model.WakeConfirmed, s.clock().UTC().Format(time.RFC3339Nano),
		key.AgentID, key.HostID, key.UID, key.EventSeq, key.Epoch, token, model.WakeSent, model.WakeDelivered)
	return checkWakeWrite(result, err, "confirm acknowledged stalled wake")
}
