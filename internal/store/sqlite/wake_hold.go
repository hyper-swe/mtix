// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeRegistrationMark reads one registration's own high-water mark and hold flag
// (FR-20.3, ADR-007 §5.4). It never reads another registration's mark (I10).
func (s *Store) WakeRegistrationMark(ctx context.Context, key model.WakeKey) (model.WakeRegistrationMark, error) {
	var mark model.WakeRegistrationMark
	var hold int64
	// Read only the caller's own (agent, host, uid) row: the mark is per registration.
	err := s.readDB.QueryRowContext(ctx,
		`SELECT epoch, high_water, pending_hold FROM agent_registrations
		  WHERE agent_id = ? AND host_id = ? AND uid = ?`,
		key.AgentID, key.HostID, key.UID).Scan(&mark.Epoch, &mark.HighWater, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WakeRegistrationMark{}, nil
	}
	if err != nil {
		return model.WakeRegistrationMark{}, fmt.Errorf("read wake registration mark %q: %w", key.AgentID, err)
	}
	mark.Found, mark.Held = true, hold != 0
	return mark, nil
}

// SetWakeHold records a hold decision for the key's registration epoch. The high-water
// mark moves forward only (never back) and only to advanceTo; zero leaves it unchanged.
// The hold is cleared only by an advance that exceeds the registration's current mark: a
// stale clear from a concurrent pass (advanceTo at or below the mark) leaves it set.
// An advance that would move the mark past the lowest unacknowledged event while a hold is
// pending is refused in the same write transaction with model.ErrWakeHeldEventMissing, so a
// pass that omitted the held event can never lift a hold another pass just recorded.
// A row of a different epoch is left untouched, so a stale dispatcher cannot move a
// re-registered session's mark (FR-20.3, I10).
func (s *Store) SetWakeHold(ctx context.Context, key model.WakeKey, advanceTo int64, hold bool) error {
	flag := 0
	if hold {
		flag = 1
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if !hold && advanceTo > 0 {
			if guardErr := guardHeldAdvance(ctx, tx, key, advanceTo); guardErr != nil {
				return guardErr
			}
		}
		// Advance the mark monotonically and set the hold flag in one write, epoch-guarded.
		// Column references on the right read the row before this update, so the hold clears
		// only when advanceTo exceeds the current mark.
		_, execErr := tx.ExecContext(ctx,
			`UPDATE agent_registrations
			    SET high_water = MAX(high_water, ?),
			        pending_hold = CASE WHEN ? = 1 THEN 1 WHEN ? > high_water THEN 0 ELSE pending_hold END
			  WHERE agent_id = ? AND host_id = ? AND uid = ? AND epoch = ?`,
			advanceTo, flag, advanceTo, key.AgentID, key.HostID, key.UID, key.Epoch)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("set wake hold %q: %w", key.AgentID, err)
	}
	return nil
}

// HeldWakeCandidates lists, for each registration of this host and uid with a pending hold
// (all agents when agent is empty), the unacknowledged addressed events above that registration's own
// high-water mark, oldest first (FR-20.3, ADR-007 section 5.4).
func (s *Store) HeldWakeCandidates(ctx context.Context, hostID, uid, agent string) ([]model.WakeKey, error) {
	// Held registrations of this host and uid only (I31); every filter is a bound parameter.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT agent_id, host_id, uid, epoch, high_water FROM agent_registrations
		  WHERE pending_hold = 1 AND host_id = ? AND uid = ? AND (? = '' OR agent_id = ?)
		  ORDER BY agent_id, host_id, uid`, hostID, uid, agent, agent)
	if err != nil {
		return nil, fmt.Errorf("list held registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var held []model.WakeKey
	var marks []int64
	for rows.Next() {
		var key model.WakeKey
		var highWater int64
		if scanErr := rows.Scan(&key.AgentID, &key.HostID, &key.UID, &key.Epoch, &highWater); scanErr != nil {
			return nil, fmt.Errorf("scan held registration: %w", scanErr)
		}
		held, marks = append(held, key), append(marks, highWater)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read held registrations: %w", err)
	}
	var keys []model.WakeKey
	for i, base := range held {
		events, listErr := s.InboxList(ctx, base.AgentID)
		if listErr != nil {
			return nil, fmt.Errorf("list held inbox events: %w", listErr)
		}
		for _, event := range events {
			if event.Seq > marks[i] {
				key := base
				key.EventSeq = event.Seq
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

// guardHeldAdvance refuses, inside the advance's own write transaction, an advance past the
// lowest unacknowledged event while the registration holds (fail closed: neither mark nor
// hold moves). The write connection is single, so no hold can be set between this read and
// the update (FR-20.3, ADR-007 section 2.7).
func guardHeldAdvance(ctx context.Context, tx *sql.Tx, key model.WakeKey, advanceTo int64) error {
	var mark, hold int64
	// Read the pre-update mark and hold of the key's own registration epoch.
	err := tx.QueryRowContext(ctx,
		`SELECT high_water, pending_hold FROM agent_registrations
		  WHERE agent_id = ? AND host_id = ? AND uid = ? AND epoch = ?`,
		key.AgentID, key.HostID, key.UID, key.Epoch).Scan(&mark, &hold)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && hold == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read hold before advance: %w", err)
	}
	lowest, found, err := lowestEligibleAbove(ctx, tx, key.AgentID, mark)
	if err != nil {
		return err
	}
	if found && lowest < advanceTo {
		return fmt.Errorf("advance to %d passes held event %d: %w", advanceTo, lowest, model.ErrWakeHeldEventMissing)
	}
	return nil
}

// lowestEligibleAbove returns the lowest unacknowledged addressed event above the mark,
// read through the given querier (a transaction or the read pool).
func lowestEligibleAbove(ctx context.Context, q inboxQuerier, agent string, above int64) (int64, bool, error) {
	rows, err := queryInboxEligible(ctx, q, agent)
	if err != nil {
		return 0, false, fmt.Errorf("list inbox for held wake event: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() { // ordered by seq ascending: the first event above the mark is the lowest
		var seq int64
		var skip [5]any
		if scanErr := rows.Scan(&seq, &skip[0], &skip[1], &skip[2], &skip[3], &skip[4]); scanErr != nil {
			return 0, false, fmt.Errorf("scan held wake event: %w", scanErr)
		}
		if seq > above {
			return seq, true, nil
		}
	}
	if err = rows.Err(); err != nil {
		return 0, false, fmt.Errorf("read held wake event: %w", err)
	}
	return 0, false, nil
}

// HeldWakeEvent returns the lowest unacknowledged addressed event of the key's agent above
// the given mark: the event a pending hold protects (FR-20.3, ADR-007 section 2.7). found is
// false when no such event exists. Read failures are returned so callers fail closed.
func (s *Store) HeldWakeEvent(ctx context.Context, key model.WakeKey, above int64) (int64, bool, error) {
	if key.AgentID == "" {
		return 0, false, fmt.Errorf("held wake event: agent id required: %w", model.ErrInvalidInput)
	}
	return lowestEligibleAbove(ctx, s.readDB, key.AgentID, above)
}
