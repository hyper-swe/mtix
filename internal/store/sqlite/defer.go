// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// deferRequest carries one defer through its transaction (MTIX-95.22).
type deferRequest struct {
	id     string
	until  sql.NullString // UTC RFC 3339; NULL when no wake time was given
	reason string
	author string
	now    time.Time
}

// DeferNode transitions a node to deferred and stores its wake time
// (defer_until) in the same transaction, per FR-3.8 and FR-3.8b (MTIX-95.22).
//
// until is stored as UTC RFC 3339 in whole seconds, the form the auto-wake
// query and the claim check compare against; nil stores NULL, clearing any
// wake time left by an earlier deferral. The transition, its activity entry
// and its sync event are exactly those of TransitionStatus: the 0.5.x event
// stays a transition_status event and does not carry the wake time.
//
// Re-deferring an already-deferred node updates defer_until and updated_at
// and records a status_change activity entry, but emits no sync event. A
// re-defer that leaves the wake time unchanged writes nothing (FR-7.7a).
//
// Returns ErrInvalidInput if until's UTC year is outside 1..9999, text no
// reader could parse back (model.IsStorableTime); this guard holds whatever
// path called. Returns ErrInvalidTransition if the node cannot be deferred
// from its current status, and ErrNotFound if it does not exist or is
// soft-deleted.
func (s *Store) DeferNode(ctx context.Context, id string, until *time.Time, reason, author string) error {
	if until != nil && !model.IsStorableTime(*until) {
		return fmt.Errorf("defer %s: until's UTC year must be 1 to 9999: %w", id, model.ErrInvalidInput)
	}
	req := deferRequest{
		id:     id,
		until:  nullableTime(until),
		reason: reason,
		author: author,
		now:    s.clock(),
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return executeDeferTx(ctx, tx, req)
	})
}

// executeDeferTx performs a defer inside the caller's transaction (MTIX-95.22).
func executeDeferTx(ctx context.Context, tx *sql.Tx, req deferRequest) error {
	fromStatus, _, err := readNodeStatus(ctx, tx, req.id)
	if err != nil {
		return err
	}
	if fromStatus == model.StatusDeferred {
		return redeferTx(ctx, tx, req)
	}

	if err := executeTransitionTx(ctx, tx, req.id, model.StatusDeferred, req.reason, req.author); err != nil {
		return err
	}

	// Store the wake time with the transition: the given time, or NULL so a
	// wake time left by an earlier deferral cannot wake this one.
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ? AND deleted_at IS NULL`,
		req.until, req.id,
	); err != nil {
		return fmt.Errorf("store defer_until for %s: %w", req.id, err)
	}
	return nil
}

// redeferTx updates the wake time of a node that is already deferred
// (MTIX-95.22). Per FR-7.7a it records a status_change activity entry with
// metadata {defer_until_changed: {from, to}} (null for no wake time), and an
// unchanged wake time is a no-op. It emits no sync event: in 0.5.x peers learn
// only the deferral, never the wake time, so a second event would carry
// nothing new.
func redeferTx(ctx context.Context, tx *sql.Tx, req deferRequest) error {
	var current sql.NullString
	// Read the stored wake time to detect a duplicate request.
	if err := tx.QueryRowContext(ctx,
		`SELECT defer_until FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		req.id,
	).Scan(&current); err != nil {
		return fmt.Errorf("read defer_until for %s: %w", req.id, err)
	}
	if current == req.until {
		return nil
	}

	// Replace the wake time and bump updated_at so change polling sees it.
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		req.until, req.now.UTC().Format(time.RFC3339), req.id,
	); err != nil {
		return fmt.Errorf("update defer_until for %s: %w", req.id, err)
	}

	if err := appendActivityEntry(ctx, tx, req.id, model.ActivityEntry{
		ID:        fmt.Sprintf("act-%d", req.now.UnixNano()),
		Type:      model.ActivityTypeStatusChange,
		Author:    req.author,
		Text:      req.reason,
		CreatedAt: req.now,
		Metadata: mustMarshal(map[string]any{
			"defer_until_changed": map[string]any{
				"from": nullableValue(current),
				"to":   nullableValue(req.until),
			},
		}),
	}); err != nil {
		return fmt.Errorf("record activity for %s: %w", req.id, err)
	}
	return nil
}

// nullableValue returns the string of a nullable column, or nil for NULL, so
// it marshals to JSON null.
func nullableValue(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

// storedDeferUntil returns the defer_until column value for a wake time
// received through sync (MTIX-95.22): UTC RFC 3339 in whole seconds, or nil
// (NULL, no wake time) when there is none or its UTC year is outside
// 1..9999, which RFC 3339 text cannot hold readably. A peer's out-of-range
// wake time is dropped rather than failing the event, which would stop the
// pull batch.
func storedDeferUntil(until *time.Time) any {
	if until == nil || !model.IsStorableTime(*until) {
		return nil
	}
	return until.UTC().Format(time.RFC3339)
}

// WakeDeferredNode reopens node id if its wake time has passed, for the wake
// pass (FR-3.8b, MTIX-95.22). Inside the waking transaction it re-checks that
// the node is still deferred with defer_until <= now; a claim, re-defer,
// cancel or delete that landed after the pass selected the node fails the
// check, and the node is left alone (false). A woken node transitions to open
// as "system", which clears its wake time and emits the usual
// transition_status event.
func (s *Store) WakeDeferredNode(ctx context.Context, id string, now time.Time) (bool, error) {
	nowStr := now.UTC().Format(time.RFC3339)
	woken := false
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var due int
		// Is the node still deferred with a wake time at or before now?
		// Both sides are UTC RFC 3339 text in whole seconds.
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes
			 WHERE id = ? AND deleted_at IS NULL AND status = ?
			   AND defer_until IS NOT NULL AND defer_until <= ?`,
			id, string(model.StatusDeferred), nowStr,
		).Scan(&due); err != nil {
			return fmt.Errorf("re-check wake of %s: %w", id, err)
		}
		if due == 0 {
			return nil
		}
		woken = true
		return executeTransitionTx(ctx, tx, id, model.StatusOpen,
			"Auto-reopened: defer_until has passed", "system")
	})
	if err != nil {
		return false, err
	}
	return woken, nil
}
