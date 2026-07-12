// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Hook dispatch ledger (FR-20 / MTIX-56.1). The ledger is the exactly-once-
// per-host primitive: every trigger — daemon tick, on-commit callback, CLI
// post-command — races to claim a (hook_name, event_seq) pair before firing
// it, and the PK guarantees one winner. Claim-then-fire is at-least-once: a
// trigger that crashes after claiming leaves a stale 'claimed' row that a
// later pass reclaims and re-fires (a lost wake is worse than a double wake;
// wake execs are documented idempotent). A terminal outcome — delivered,
// error, skipped-untrusted, rate-limited — is final and never re-fired.

// HookDispatchOutcome values recorded in the ledger. OutcomeClaimed is the
// only non-terminal state.
const (
	OutcomeClaimed          = "claimed"
	OutcomeDelivered        = "delivered"
	OutcomeError            = "error"
	OutcomeSkippedUntrusted = "skipped-untrusted"
	OutcomeRateLimited      = "rate-limited"
)

// HookClaim identifies one ledger row — the unit of the reclaim scan.
type HookClaim struct {
	Hook string
	Seq  int64
}

// ClaimHookDispatch attempts to claim (hook, seq) for firing. It returns true
// iff the CALLER won and must proceed to fire (then record the outcome):
// either the pair's previous claim is stale — still 'claimed' with fired_at
// older than lease, i.e. a trigger crashed between claim and fire — or the
// pair was never claimed AND seq is above the scan floor. Any terminal
// outcome, a claim fresher than the lease, or a fresh claim at/below the
// floor loses.
//
// The floor check closes the prune race: a trigger holding a floor snapshot
// from before a concurrent pass advanced-and-pruned would otherwise re-INSERT
// a pruned (hook,seq) and double-fire it. Below the floor, "no row" means
// "terminally dispatched and compacted", never "new" — the floor only passes
// terminal rows (AdvanceHookScanFloorClamped) and pruning only removes them.
// A stale 'claimed' ROW below the floor remains reclaimable: crash recovery
// is keyed on the row existing, not on the floor.
func (s *Store) ClaimHookDispatch(ctx context.Context, hook string, seq int64, lease time.Duration) (bool, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-lease).Format(time.RFC3339)
	var won bool
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Reclaim branch: take over a stale claim (crashed trigger).
		res, err := tx.ExecContext(ctx, `
			UPDATE hook_dispatch_ledger SET fired_at = ?
			 WHERE hook_name = ? AND event_seq = ?
			   AND outcome = ? AND fired_at < ?`,
			now.Format(time.RFC3339), hook, seq, OutcomeClaimed, cutoff)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n > 0 {
			won = true
			return nil
		}
		// Any surviving row — terminal or in-lease claim — means we lose.
		var exists int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM hook_dispatch_ledger
			 WHERE hook_name = ? AND event_seq = ?`, hook, seq).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return nil
		}
		// Fresh claim: refused at/below the floor (dispatched + pruned).
		var floor sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT cursor FROM hook_dispatch_cursor WHERE id = 1`).Scan(&floor); err != nil && err != sql.ErrNoRows {
			return err
		}
		if floor.Valid && seq <= floor.Int64 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hook_dispatch_ledger (hook_name, event_seq, fired_at, outcome)
			VALUES (?, ?, ?, ?)`,
			hook, seq, now.Format(time.RFC3339), OutcomeClaimed); err != nil {
			return err
		}
		won = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("claim hook dispatch (%s, %d): %w", hook, seq, err)
	}
	return won, nil
}

// RecordHookDispatchOutcome finalizes a claimed row with how the fire ended.
// Recording onto a row another trigger has since reclaimed is harmless — the
// last writer's outcome stands; both fired, which at-least-once permits.
func (s *Store) RecordHookDispatchOutcome(ctx context.Context, hook string, seq int64, outcome string) error {
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE hook_dispatch_ledger SET outcome = ?, fired_at = ?
			 WHERE hook_name = ? AND event_seq = ?`,
			outcome, time.Now().UTC().Format(time.RFC3339), hook, seq)
		return err
	})
	if err != nil {
		return fmt.Errorf("record hook dispatch outcome (%s, %d): %w", hook, seq, err)
	}
	return nil
}

// StaleHookClaims returns every 'claimed' row older than lease — triggers that
// crashed between claim and fire. It is deliberately NOT bounded by the scan
// floor: a claim can slip below the floor in a narrow advance race, and this
// scan (plus pruning never deleting 'claimed' rows) is what still finds it.
func (s *Store) StaleHookClaims(ctx context.Context, lease time.Duration) ([]HookClaim, error) {
	cutoff := time.Now().Add(-lease).UTC().Format(time.RFC3339)
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT hook_name, event_seq FROM hook_dispatch_ledger
		 WHERE outcome = ? AND fired_at < ?
		 ORDER BY event_seq ASC`, OutcomeClaimed, cutoff)
	if err != nil {
		return nil, fmt.Errorf("stale hook claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HookClaim
	for rows.Next() {
		var c HookClaim
		if err := rows.Scan(&c.Hook, &c.Seq); err != nil {
			return nil, fmt.Errorf("scan stale hook claim: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AdvanceHookScanFloorClamped moves the scan floor toward target, clamped
// below the lowest open ('claimed') ledger row so a crashed trigger's event
// stays inside the scan window until reclaimed. Monotonic: never rewinds.
func (s *Store) AdvanceHookScanFloorClamped(ctx context.Context, target int64) error {
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var lowestOpen sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT MIN(event_seq) FROM hook_dispatch_ledger WHERE outcome = ?`,
			OutcomeClaimed).Scan(&lowestOpen); err != nil {
			return err
		}
		floor := target
		if lowestOpen.Valid && lowestOpen.Int64-1 < floor {
			floor = lowestOpen.Int64 - 1
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hook_dispatch_cursor (id, cursor) VALUES (1, ?)
			ON CONFLICT(id) DO UPDATE SET cursor = MAX(cursor, excluded.cursor)`, floor)
		return err
	})
	if err != nil {
		return fmt.Errorf("advance hook scan floor to %d: %w", target, err)
	}
	return nil
}

// PruneHookDispatchLedger deletes terminal rows at or below the scan floor
// (compaction, like the MTIX-55 ack ledger). 'claimed' rows are never pruned:
// they are the only evidence a crashed trigger owes a re-fire.
func (s *Store) PruneHookDispatchLedger(ctx context.Context) error {
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM hook_dispatch_ledger
			 WHERE outcome <> ?
			   AND event_seq <= (SELECT COALESCE(MAX(cursor), 0) FROM hook_dispatch_cursor WHERE id = 1)`,
			OutcomeClaimed)
		return err
	})
	if err != nil {
		return fmt.Errorf("prune hook dispatch ledger: %w", err)
	}
	return nil
}

// JournalTail returns the highest sync_events rowid (0 when the journal is
// empty) — the bootstrap floor-init input (FR-20 §8: a store initialized with
// pulled history starts its floor at the tail so history is never a backlog).
func (s *Store) JournalTail(ctx context.Context) (int64, error) {
	var tail sql.NullInt64
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT MAX(rowid) FROM sync_events`).Scan(&tail); err != nil {
		return 0, fmt.Errorf("journal tail: %w", err)
	}
	return tail.Int64, nil
}

// InitHookScanFloorAtTail jumps the scan floor to the current journal tail —
// the bootstrap call for a store whose journal was populated before any
// dispatch state existed (fresh clone / first pull into an empty store), so
// pulled history arrives pre-dispatched instead of as a hook backlog storm
// (FR-20 §8). Monotonic like every floor advance; a no-op on a live store
// whose floor already tracks the tail.
func (s *Store) InitHookScanFloorAtTail(ctx context.Context) error {
	tail, err := s.JournalTail(ctx)
	if err != nil {
		return err
	}
	return s.AdvanceHookScanFloorClamped(ctx, tail)
}

// ReadJournalEventAt returns the single journal event at seq (ok=false when it
// does not exist — e.g. a ledger row for a pruned/foreign journal). It feeds
// the stale-claim re-fire, which needs one event rather than a range.
func (s *Store) ReadJournalEventAt(ctx context.Context, seq int64) (JournalEvent, bool, error) {
	events, err := s.ReadJournalSince(ctx, seq-1, 1)
	if err != nil {
		return JournalEvent{}, false, err
	}
	if len(events) == 0 || events[0].Seq != seq {
		return JournalEvent{}, false, nil
	}
	return events[0], true, nil
}
