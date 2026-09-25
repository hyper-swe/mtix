// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
// applied: the Lamport and vector clocks of the LOCAL sync_events row are
// merged and the event is recorded in applied_events (INSERT OR IGNORE)
// with the local row's Lamport clock, but it is never dispatched, never
// mirrored and never logged as a conflict. The clocks come from the local
// row, never from the pulled copy (MTIX-95.11): the hub row of an event
// this replica pushed can be changed on the hub, and the local row is what
// this replica emitted, so a changed copy cannot move the local clocks. Applying it
// again would log a spurious LWW conflict against this replica's own
// history and let a replayed claim, unclaim, defer or transition_status
// overwrite newer local state.
//
// No sync_events row is added, so hook dispatch and inbox delivery still see
// the event exactly once, and the hook journal's Synced flag (derived from
// applied_events) keeps its meaning.
//
// Returns held=false, having written nothing, for an event this replica does
// not hold; the caller then applies it through dispatchWithLWW (field LWW,
// or the workflow winner rule for workflow events, MTIX-95.10).
func acknowledgeHeldEvent(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) (bool, error) {
	local, err := readHeldEvent(ctx, tx, event.EventID)
	if err != nil {
		return false, fmt.Errorf("apply %s: own-event check: %w", event.EventID, err)
	}
	if local == nil {
		return false, nil
	}
	if err := advanceLamport(ctx, tx, local.LamportClock); err != nil {
		return true, fmt.Errorf("apply %s: held event: advance lamport: %w", event.EventID, err)
	}
	if err := mergeVectorClock(ctx, tx, local.AuthorID, local.VectorClock); err != nil {
		return true, fmt.Errorf("apply %s: held event: merge VC: %w", event.EventID, err)
	}
	if err := recordApplied(ctx, tx, local); err != nil {
		return true, fmt.Errorf("apply %s: held event: record applied: %w", event.EventID, err)
	}
	return true, nil
}

// readHeldEvent returns the clocks of eventID's row in the local
// sync_events log (MTIX-95.2), or nil when this replica does not hold it.
// Any sync_status counts: pending, pushed or conflicted (this replica
// emitted it) and applied (it was mirrored from the hub). The returned
// event carries only EventID, LamportClock, VectorClock and AuthorID: what
// acknowledgeHeldEvent merges and records (MTIX-95.11).
func readHeldEvent(ctx context.Context, tx *sql.Tx, eventID string) (*model.SyncEvent, error) {
	held := model.SyncEvent{EventID: eventID}
	var vc string
	// Primary-key lookup on sync_events.event_id; the status is irrelevant.
	err := tx.QueryRowContext(ctx,
		`SELECT lamport_clock, vector_clock, author_id FROM sync_events WHERE event_id = ?`, eventID,
	).Scan(&held.LamportClock, &vc, &held.AuthorID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query sync_events: %w", err)
	}
	if err := json.Unmarshal([]byte(vc), &held.VectorClock); err != nil {
		return nil, fmt.Errorf("decode local vector_clock: %w", err)
	}
	return &held, nil
}
