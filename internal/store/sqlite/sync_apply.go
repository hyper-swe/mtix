// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// IdempotentApply applies a single sync event to the local nodes table
// per FR-18.9 / SYNC-DESIGN section 8. Symmetric to emitEvent: emit
// writes our own mutations into sync_events; apply consumes events
// pulled from the hub and replays them against the nodes table.
//
// MUST be called inside an open *sql.Tx (caller's responsibility, same
// as emitEvent). The caller is the apply loop in MTIX-15.7's mtix sync
// pull; until that lands, the only callers are the tests in this
// package.
//
// Behavior:
//   - Valid duplicate event_id: silent no-op (FR-18.9 idempotency).
//   - Validates the event before any mutation; invalid events surface
//     ErrInvalidInput.
//   - An event already in the local sync_events log (any sync_status;
//     typically this replica's own event returned by a pull) is
//     acknowledged, not applied: clocks merged, applied_events recorded,
//     no dispatch, no mirror row, no conflict (MTIX-95.2, see
//     acknowledgeHeldEvent).
//   - Dispatches on op_type to a per-op apply function.
//   - DOES NOT emit a sync_event (apply MUST NOT loop).
//   - Always advances local Lamport to max(local, event.lamport) and
//     merges event.author_id into the local vector clock.
//   - Records the event in applied_events on success.
func IdempotentApply(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	// A store with only a clock records the history of the replay, as Store.IdempotentApply does.
	return (&Store{clock: time.Now}).IdempotentApply(ctx, tx, event)
}

// IdempotentApply applies an event in the caller's transaction using this
// store's clock (FR-18.9). SetClock must be called before concurrent use.
func (s *Store) IdempotentApply(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	if event == nil {
		return fmt.Errorf("apply: event nil: %w", model.ErrInvalidInput)
	}
	// MTIX-95.31.19.3: the replay's field changes are recorded with origin hub and the event id.
	// The scope is the task the event resolves to (by uid first, as the handlers do), not its number.
	scope := historyScope{id: replayTarget(ctx, tx, event), actor: event.AuthorID, origin: model.HistoryOriginHub, eventID: event.EventID}
	return s.inTxHistory(ctx, tx, scope, func() error {
		return (syncApplier{clock: clockOrDefault(s.clock), store: s}).IdempotentApply(ctx, tx, event)
	})
}

// syncApplier carries the apply-time clock through all replay writes.
// Event wall-clock timestamps still determine eventTime and LWW ordering.
type syncApplier struct {
	clock func() time.Time
	store *Store // records the history of tasks the replay reaches; nil outside a Store
}

func (a syncApplier) IdempotentApply(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	if event == nil {
		return fmt.Errorf("apply: event nil: %w", model.ErrInvalidInput)
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("apply %s: %w", event.EventID, err)
	}

	if payloadErr := validateIssueTypeEvent(event); payloadErr != nil {
		return fmt.Errorf("apply %s: classification payload: %w", event.EventID, payloadErr)
	}

	already, err := isAppliedEvent(ctx, tx, event.EventID)
	if err != nil {
		return fmt.Errorf("apply %s: dedupe check: %w", event.EventID, err)
	}
	if already {
		return nil
	}

	// Own-event rule (MTIX-95.2): an event this replica already holds is
	// acknowledged and never re-applied. Foreign events fall through.
	if held, heldErr := a.acknowledgeHeldEvent(ctx, tx, event); heldErr != nil || held {
		return heldErr
	}

	// Mirror the event into the local sync_events log so subsequent
	// LWW lookups find it. Only a foreign event reaches this point: an
	// event already in the log returned above.
	if mirrorErr := a.mirrorIncomingEvent(ctx, tx, event); mirrorErr != nil {
		return fmt.Errorf("apply %s: mirror: %w", event.EventID, mirrorErr)
	}

	// A foreign event is evidence of a hub or relay: set the sticky mark in
	// this transaction so it rolls back with a failed apply (ADR-007 A9).
	if _, markErr := setForeignHubMark(ctx, tx, event.EventID, a.clock()); markErr != nil {
		return fmt.Errorf("apply %s: hub mark: %w", event.EventID, markErr)
	}

	if err := a.dispatchWithLWW(ctx, tx, event); err != nil {
		return err
	}

	if err := advanceLamport(ctx, tx, event.LamportClock); err != nil {
		return fmt.Errorf("apply %s: advance lamport: %w", event.EventID, err)
	}
	if err := mergeVectorClock(ctx, tx, event.AuthorID, event.VectorClock); err != nil {
		return fmt.Errorf("apply %s: merge VC: %w", event.EventID, err)
	}
	if err := a.recordApplied(ctx, tx, event); err != nil {
		return fmt.Errorf("apply %s: record applied: %w", event.EventID, err)
	}
	return nil
}

// isAppliedEvent returns true iff event_id already exists in the
// applied_events table (FR-18.9 dedupe key).
func isAppliedEvent(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM applied_events WHERE event_id = ?`, eventID,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// recordApplied marks the event as applied so a re-pull-and-replay is
// a no-op. INSERT OR IGNORE handles the race where two concurrent tx
// race to apply the same event (only one wins; the other is a no-op).
func (a syncApplier) recordApplied(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO applied_events (event_id, applied_at, applied_by_lamport)
		 VALUES (?, ?, ?)`,
		event.EventID, a.clock().UTC().Format(time.RFC3339Nano), event.LamportClock,
	)
	return err
}

// advanceLamport sets meta.sync.lamport to max(current, observed).
// Lamport never goes backwards so a late-arriving event with a low
// clock cannot rewind us.
func advanceLamport(ctx context.Context, tx *sql.Tx, observed int64) error {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`,
	).Scan(&raw)
	if err != nil {
		return err
	}
	current, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("parse current lamport %q: %w", raw, err)
	}
	if observed <= current {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.lamport'`,
		strconv.FormatInt(observed, 10),
	)
	return err
}

// mergeVectorClock merges the observed vector clock into the local one
// using per-key max. The author_id is also bumped to ensure our local
// view records that we observed this author at the new timestamp.
func mergeVectorClock(ctx context.Context, tx *sql.Tx, authorID string, observed model.VectorClock) error {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.vector_clock'`,
	).Scan(&raw)
	if err != nil {
		return err
	}
	local := model.VectorClock{}
	if raw != "" && raw != "{}" && raw != "null" {
		if parseErr := json.Unmarshal([]byte(raw), &local); parseErr != nil {
			return fmt.Errorf("parse local VC %q: %w", raw, parseErr)
		}
	}
	// Exact per-key max, then the deterministic prune (MTIX-95.16): the
	// stored clock never exceeds the cap, so a later local write cannot fail
	// on it. The observed author (recorded even at 0 if the event did not
	// carry it), every author this replica emits under and the default local
	// author are kept, so no counter is lost or reset.
	merged := local.Merge(observed)
	if _, ok := merged[authorID]; !ok {
		merged[authorID] = 0
	}
	emitting, _, err := loadEmittingAuthors(ctx, tx)
	if err != nil {
		return err
	}
	keep := append([]string{authorID}, emitting...)
	keep = append(keep, sanitizeAuthorID(resolveEmitAuthor(ctx, tx, "")))
	merged, pruned := merged.Prune(keep...)
	notePrune(ctx, tx, pruned, authorID)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encode merged VC: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.vector_clock'`,
		string(encoded),
	)
	return err
}
