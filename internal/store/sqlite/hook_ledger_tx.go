// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type hookDispatchClaim struct {
	hook        string
	seq         int64
	now, cutoff string
}

func (s *Store) claimHookDispatch(ctx context.Context, hook string, event JournalEvent, lease time.Duration, guarded bool) (bool, bool, error) {
	now := s.clock().UTC()
	claim := hookDispatchClaim{hook: hook, seq: event.Seq, now: now.Format(time.RFC3339), cutoff: now.Add(-lease).Format(time.RFC3339)}
	var won bool
	matched := true
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if guarded {
			matched, err = hookJournalIdentityMatches(ctx, tx, event)
			if err != nil || !matched {
				return err
			}
		}
		won, err = claimHookDispatchTx(ctx, tx, claim)
		return err
	})
	if err != nil {
		return false, false, fmt.Errorf("claim hook dispatch (%s, %d): %w", hook, event.Seq, err)
	}
	return won, matched, nil
}

func claimHookDispatchTx(ctx context.Context, tx *sql.Tx, claim hookDispatchClaim) (bool, error) {
	// Only an expired non-terminal claim may be reclaimed.
	res, err := tx.ExecContext(ctx, `UPDATE hook_dispatch_ledger SET fired_at = ?
  WHERE hook_name = ? AND event_seq = ? AND outcome = ? AND fired_at < ?`,
		claim.now, claim.hook, claim.seq, OutcomeClaimed, claim.cutoff)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	allowed, err := canClaimHookDispatchTx(ctx, tx, claim)
	if err != nil || !allowed {
		return false, err
	}
	// A fresh pair is claimed only above the compacted scan floor.
	_, err = tx.ExecContext(ctx, `INSERT INTO hook_dispatch_ledger (hook_name,event_seq,fired_at,outcome) VALUES (?,?,?,?)`, claim.hook, claim.seq, claim.now, OutcomeClaimed)
	return err == nil, err
}

func canClaimHookDispatchTx(ctx context.Context, tx *sql.Tx, claim hookDispatchClaim) (bool, error) {
	var exists int
	// A terminal result or an unexpired owner keeps the pair unavailable.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM hook_dispatch_ledger WHERE hook_name = ? AND event_seq = ?`, claim.hook, claim.seq).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		return false, nil
	}
	var floor sql.NullInt64
	// A compacted pair below the floor must not be recreated by an old pass.
	if err := tx.QueryRowContext(ctx, `SELECT cursor FROM hook_dispatch_cursor WHERE id = 1`).Scan(&floor); err != nil && err != sql.ErrNoRows {
		return false, err
	}
	return !floor.Valid || claim.seq > floor.Int64, nil
}

func (s *Store) recordHookDispatchOutcome(ctx context.Context, hook string, event JournalEvent, outcome string, guarded bool) (bool, error) {
	matched := true
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if guarded {
			matched, err = hookJournalIdentityMatches(ctx, tx, event)
			if err != nil || !matched {
				return err
			}
		}
		// An old event's completion must never settle a reused sequence's claim.
		_, err = tx.ExecContext(ctx, `UPDATE hook_dispatch_ledger SET outcome = ?, fired_at = ? WHERE hook_name = ? AND event_seq = ?`, outcome, s.clock().UTC().Format(time.RFC3339), hook, event.Seq)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("record hook dispatch outcome (%s, %d): %w", hook, event.Seq, err)
	}
	return matched, nil
}

func advanceHookScanFloorTx(ctx context.Context, tx *sql.Tx, target int64) error {
	var lowestOpen sql.NullInt64
	// Leave every open claim inside the scan window for crash recovery.
	if err := tx.QueryRowContext(ctx, `SELECT MIN(event_seq) FROM hook_dispatch_ledger WHERE outcome = ?`, OutcomeClaimed).Scan(&lowestOpen); err != nil {
		return err
	}
	floor := target
	if lowestOpen.Valid && lowestOpen.Int64-1 < floor {
		floor = lowestOpen.Int64 - 1
	}
	// Ordinary advances are monotonic; replace alone rewinds a removed tail.
	_, err := tx.ExecContext(ctx, `INSERT INTO hook_dispatch_cursor (id,cursor) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET cursor = MAX(cursor,excluded.cursor)`, floor)
	return err
}
