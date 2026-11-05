// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ClaimHookDispatchEvent claims only if event still identifies the same journal
// row, checked in the claim's write transaction (FR-20, MTIX-112.2.2). matched
// is false on a quiet identity-fence miss; won is true only for a new owner.
func (s *Store) ClaimHookDispatchEvent(ctx context.Context, hook string, event JournalEvent, lease time.Duration) (won, matched bool, err error) {
	// A missing-row snapshot is recovery bookkeeping, never a fresh event.
	if event.EventID == "" {
		removed, err := s.discardMissingHookClaim(ctx, hook, event.Seq, lease)
		return false, removed, err
	}
	return s.claimHookDispatch(ctx, hook, event, lease, true)
}

// RecordHookDispatchEventOutcome finalizes only while event's journal identity
// still matches inside the write transaction. A mismatch returns false without
// altering a new event's claim (FR-20, MTIX-112.2.2).
func (s *Store) RecordHookDispatchEventOutcome(ctx context.Context, hook string, event JournalEvent, outcome string) (bool, error) {
	return s.recordHookDispatchOutcome(ctx, hook, event, outcome, true)
}

// hookJournalIdentityMatches requires an actual, nonempty journal identity.
// Missing-event recovery has its own delete-only transaction.
func hookJournalIdentityMatches(ctx context.Context, tx *sql.Tx, event JournalEvent) (bool, error) {
	var matches bool
	// Compare identity under the same writer lock as the guarded mutation.
	err := tx.QueryRowContext(ctx, `SELECT ? <> '' AND EXISTS(SELECT 1 FROM sync_events WHERE rowid = ? AND event_id = ?)`, event.EventID, event.Seq, event.EventID).Scan(&matches)
	if err != nil {
		return false, fmt.Errorf("check hook journal identity at %d: %w", event.Seq, err)
	}
	return matches, nil
}

// AdvanceHookScanFloorForEvents advances through only the matching prefix of
// an ascending ReadJournalSince snapshot, clamped below open claims. An old
// pass cannot consume unseen events at reused rowids. It returns the number of
// identity-fence misses (0 or 1: a rewound floor or first changed row), not an
// error for a replaced event (FR-20, MTIX-112.2.2).
func (s *Store) AdvanceHookScanFloorForEvents(ctx context.Context, observedFloor int64, events []JournalEvent) (uint64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	var misses uint64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(cursor),0) FROM hook_dispatch_cursor WHERE id=1`).Scan(&current); err != nil {
			return fmt.Errorf("read current hook scan floor: %w", err)
		}
		// Replace may run between the separate floor and journal reads. A page
		// selected above the old floor omits newly reused events below that floor.
		if current < observedFloor {
			misses = 1
			return nil
		}
		target, n, err := matchingHookJournalPrefix(ctx, tx, events)
		misses = n
		if err != nil || target == 0 {
			return err
		}
		return advanceHookScanFloorTx(ctx, tx, target)
	})
	if err != nil {
		return 0, fmt.Errorf("advance hook scan floor for journal snapshot: %w", err)
	}
	return misses, nil
}

func matchingHookJournalPrefix(ctx context.Context, tx *sql.Tx, events []JournalEvent) (target int64, misses uint64, err error) {
	// One writer snapshot covers the whole observed page, including holes from
	// removed events; stop before the first row whose identity no longer matches.
	rows, err := tx.QueryContext(ctx, `SELECT rowid,event_id FROM sync_events WHERE rowid >= ? AND rowid <= ? ORDER BY rowid`, events[0].Seq, events[len(events)-1].Seq)
	if err != nil {
		return 0, 0, fmt.Errorf("read hook journal identities: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close hook journal identities: %w", closeErr)
		}
	}()
	identities := make(map[int64]string, len(events))
	for rows.Next() {
		var seq int64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			return 0, 0, fmt.Errorf("scan hook journal identity: %w", err)
		}
		identities[seq] = id
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate hook journal identities: %w", err)
	}
	for _, event := range events {
		id, exists := identities[event.Seq]
		if !exists || id != event.EventID {
			return target, 1, nil
		}
		target = event.Seq
	}
	return target, 0, nil
}
