// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/jackc/pgx/v5"
)

// ConflictDescriptor identifies a concurrent edit detected at push
// time per SYNC-DESIGN section 8. Resolution (LWW) is a 15.5 concern;
// this transport surface only flags the existence of a conflict so
// the caller can surface it to the user via mtix sync conflicts list.
type ConflictDescriptor struct {
	NewEventID         string `json:"new_event_id"`
	ConflictingEventID string `json:"conflicting_event_id"`
	NodeID             string `json:"node_id"`
	FieldName          string `json:"field_name,omitempty"`
}

// PushEvents validates the batch and inserts each event into
// sync_events under the caller's retry/backoff envelope. Returns the
// IDs that landed (after ON CONFLICT DO NOTHING dedupe) plus any
// concurrency conflicts detected against pre-existing events.
//
// Atomicity: all events are inserted in a single PG transaction.
// Validation happens BEFORE the transaction opens; an invalid batch
// returns immediately with no PG side effects.
//
// Idempotent: re-pushing the same events is a no-op (the event_id PK
// + ON CONFLICT DO NOTHING short-circuits duplicates).
func (p *Pool) PushEvents(ctx context.Context, events []*model.SyncEvent) (
	acceptedIDs []string, conflicts []ConflictDescriptor, err error,
) {
	if len(events) == 0 {
		return nil, nil, nil
	}
	// Validate before touching the pool: caller-side bugs surface even
	// when the pool is misconfigured.
	if vErr := validator.ValidateBatch(events, time.Now().UTC(), nil); vErr != nil {
		return nil, nil, fmt.Errorf("PushEvents validate: %w", vErr)
	}
	if p == nil || p.p == nil {
		return nil, nil, fmt.Errorf("PushEvents: pool not open")
	}

	cfg := DefaultRetryConfig()
	err = retryWithBackoff(ctx, cfg, func(ctx context.Context) error {
		ids, conf, opErr := p.pushEventsOnce(ctx, events)
		if opErr != nil {
			return opErr
		}
		acceptedIDs = ids
		conflicts = conf
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("PushEvents: %w", err)
	}
	return acceptedIDs, conflicts, nil
}

// pushEventsOnce runs one PG transaction's worth of pushes. Wrapped
// by retryWithBackoff in PushEvents.
func (p *Pool) pushEventsOnce(ctx context.Context, events []*model.SyncEvent) (
	[]string, []ConflictDescriptor, error,
) {
	tx, err := p.p.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	accepted := make([]string, 0, len(events))
	conflicts := make([]ConflictDescriptor, 0)

	for _, e := range events {
		conf, err := detectConflicts(ctx, tx, e)
		if err != nil {
			return nil, nil, fmt.Errorf("detect conflicts for %s: %w", e.EventID, err)
		}
		conflicts = append(conflicts, conf...)

		vcJSON, err := json.Marshal(e.VectorClock)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal VC for %s: %w", e.EventID, err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO sync_events
			  (event_id, project_prefix, node_id, op_type, payload,
			   wall_clock_ts, lamport_clock, vector_clock,
			   author_id, author_machine_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (event_id) DO NOTHING`,
			e.EventID, e.ProjectPrefix, e.NodeID, string(e.OpType), string(e.Payload),
			e.WallClockTS, e.LamportClock, string(vcJSON),
			e.AuthorID, e.AuthorMachineHash,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("insert %s: %w", e.EventID, err)
		}
		if tag.RowsAffected() == 1 {
			accepted = append(accepted, e.EventID)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	return accepted, conflicts, nil
}

// detectConflicts looks for prior events on the same node that are
// concurrent with e per VectorClock.Concurrent. Returns descriptors
// for every concurrent prior; resolution is a 15.5 concern.
//
// Scoping: only field-level updates (update_field, set_acceptance,
// set_prompt) are checked, since other op_types (claim, transition,
// delete, link_dep, etc.) have natural single-row outcomes that don't
// produce field-level conflicts.
func detectConflicts(ctx context.Context, tx pgx.Tx, e *model.SyncEvent) ([]ConflictDescriptor, error) {
	switch e.OpType {
	case model.OpUpdateField, model.OpSetAcceptance, model.OpSetPrompt:
	default:
		return nil, nil
	}
	fieldName, _ := extractFieldName(e)

	rows, err := tx.Query(ctx, `
		SELECT event_id, vector_clock
		FROM sync_events
		WHERE node_id = $1
		  AND op_type IN ('update_field','set_acceptance','set_prompt')
		  AND event_id <> $2`,
		e.NodeID, e.EventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConflictDescriptor
	for rows.Next() {
		var priorID, priorVCJSON string
		if err := rows.Scan(&priorID, &priorVCJSON); err != nil {
			return nil, err
		}
		var priorVC model.VectorClock
		if err := json.Unmarshal([]byte(priorVCJSON), &priorVC); err != nil {
			continue // skip un-parseable; not a conflict by definition
		}
		if e.VectorClock.Concurrent(priorVC) {
			out = append(out, ConflictDescriptor{
				NewEventID:         e.EventID,
				ConflictingEventID: priorID,
				NodeID:             e.NodeID,
				FieldName:          fieldName,
			})
		}
	}
	return out, rows.Err()
}

// extractFieldName pulls the field_name from update_field payloads
// for conflict-descriptor decoration. Returns "" for set_acceptance /
// set_prompt (the op_type is the field).
func extractFieldName(e *model.SyncEvent) (string, error) {
	if e.OpType != model.OpUpdateField {
		return "", nil
	}
	var p model.UpdateFieldPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return "", err
	}
	return p.FieldName, nil
}

// PullEvents returns up to limit events with lamport_clock greater
// than sinceLamport, in ascending order. The hasMore flag is true
// when there are events past the returned page.
//
// Wrapped in the retry envelope so transient PG failures don't fail
// the caller's pull loop. Validation NOT applied here — events on the
// hub are already validated; pull is a read.
func (p *Pool) PullEvents(ctx context.Context, sinceLamport int64, limit int) (
	events []*model.SyncEvent, hasMore bool, err error,
) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("PullEvents: limit must be > 0")
	}
	if p == nil || p.p == nil {
		return nil, false, fmt.Errorf("PullEvents: pool not open")
	}

	cfg := DefaultRetryConfig()
	err = retryWithBackoff(ctx, cfg, func(ctx context.Context) error {
		evs, more, opErr := p.pullEventsOnce(ctx, sinceLamport, limit)
		if opErr != nil {
			return opErr
		}
		events = evs
		hasMore = more
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("PullEvents: %w", err)
	}
	return events, hasMore, nil
}

func (p *Pool) pullEventsOnce(ctx context.Context, sinceLamport int64, limit int) (
	[]*model.SyncEvent, bool, error,
) {
	rows, err := p.p.Query(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash, created_at
		FROM sync_events
		WHERE lamport_clock > $1
		ORDER BY lamport_clock
		LIMIT $2`,
		sinceLamport, limit+1,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]*model.SyncEvent, 0, limit)
	for rows.Next() {
		var e model.SyncEvent
		var opType, payload, vc string
		var createdAt time.Time
		if err := rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash, &createdAt,
		); err != nil {
			return nil, false, err
		}
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		e.CreatedAt = createdAt
		if err := json.Unmarshal([]byte(vc), &e.VectorClock); err != nil {
			return nil, false, fmt.Errorf("decode VC for %s: %w", e.EventID, err)
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

