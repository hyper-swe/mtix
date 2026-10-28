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
// A row of a different epoch is left untouched, so a stale dispatcher cannot move a
// re-registered session's mark (FR-20.3, I10).
func (s *Store) SetWakeHold(ctx context.Context, key model.WakeKey, advanceTo int64, hold bool) error {
	flag := 0
	if hold {
		flag = 1
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Advance the mark monotonically and set the hold flag in one write, epoch-guarded.
		_, execErr := tx.ExecContext(ctx,
			`UPDATE agent_registrations
			    SET high_water = MAX(high_water, ?), pending_hold = ?
			  WHERE agent_id = ? AND host_id = ? AND uid = ? AND epoch = ?`,
			advanceTo, flag, key.AgentID, key.HostID, key.UID, key.Epoch)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("set wake hold %q: %w", key.AgentID, err)
	}
	return nil
}
