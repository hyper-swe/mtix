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

// acknowledgeHeldEvent implements the own-event rule of IdempotentApply
// (FR-18.9 idempotency; MTIX-95.2, ADR-006 D1-D3).
//
// The hub's pull returns every event past the cursor, including the events
// this replica pushed itself. Those never pass through apply when they are
// emitted, so applied_events alone cannot recognize them. An event whose
// event_id is already in the local sync_events log, whatever its
// sync_status, is one this replica already holds. It is acknowledged, not
// applied: the Lamport and vector clocks are merged and the event is
// recorded in applied_events (INSERT OR IGNORE), but it is never
// dispatched, never mirrored and never logged as a conflict. Applying it
// again would log a spurious LWW conflict against this replica's own
// history and let a replayed claim, unclaim, defer or transition_status
// overwrite newer local state.
//
// No sync_events row is added, so hook dispatch and inbox delivery still see
// the event exactly once, and the hook journal's Synced flag (derived from
// applied_events) keeps its meaning.
//
// Returns held=false, having written nothing, for an event this replica does
// not hold; the caller then applies it through the unchanged LWW path.
func acknowledgeHeldEvent(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) (bool, error) {
	held, err := isHeldEvent(ctx, tx, event.EventID)
	if err != nil {
		return false, fmt.Errorf("apply %s: own-event check: %w", event.EventID, err)
	}
	if !held {
		return false, nil
	}
	if err := advanceLamport(ctx, tx, event.LamportClock); err != nil {
		return true, fmt.Errorf("apply %s: held event: advance lamport: %w", event.EventID, err)
	}
	if err := mergeVectorClock(ctx, tx, event.AuthorID, event.VectorClock); err != nil {
		return true, fmt.Errorf("apply %s: held event: merge VC: %w", event.EventID, err)
	}
	if err := recordApplied(ctx, tx, event); err != nil {
		return true, fmt.Errorf("apply %s: held event: record applied: %w", event.EventID, err)
	}
	return true, nil
}

// isHeldEvent reports whether eventID is already in the local sync_events
// log (MTIX-95.2). Any sync_status counts: pending, pushed or conflicted
// (this replica emitted it) and applied (it was mirrored from the hub).
func isHeldEvent(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	var one int
	// Primary-key lookup on sync_events.event_id; the status is irrelevant.
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM sync_events WHERE event_id = ?`, eventID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query sync_events: %w", err)
	}
	return true, nil
}
