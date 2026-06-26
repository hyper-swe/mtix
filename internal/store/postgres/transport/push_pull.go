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
// This is the field-level-conflict view kept for callers that predate
// the node registry. It DISCARDS the renumber-required outcomes; callers
// that need them (the claim flow per ADR-003 §6) MUST use
// PushEventsWithRenumbers. A renumber-required create is simply not
// listed in acceptedIDs here — no node is lost, but the caller is not
// told to retry the number.
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
	acceptedIDs, conflicts, _, err = p.PushEventsWithRenumbers(ctx, events)
	return acceptedIDs, conflicts, err
}

// PushEventsWithRenumbers is PushEvents plus the structured
// renumber-required outcomes from the node registry (ADR-003 §6).
//
// An incoming create_node whose (project_prefix, display_path) is already
// registered (by a DIFFERENT create event — first-writer-wins) is NOT
// inserted and is returned in renumbers so the claimer can retry the next
// free number. Re-pushing the SAME create event (same event_id) is an
// idempotent no-op, never a renumber.
//
// A renumber-required result NEVER loses a node: the rejected node stays
// in the pusher's canonical local store; only its display number must
// move. Per ADR-003 §9 the registry is liveness, not a security boundary.
//
// Block scope (ADR-003 §6.1/F-1): one node's collision does NOT wedge the
// batch — every other event in the same push still lands.
//
// Atomicity and idempotency match PushEvents.
func (p *Pool) PushEventsWithRenumbers(ctx context.Context, events []*model.SyncEvent) (
	acceptedIDs []string, conflicts []ConflictDescriptor, renumbers []RenumberRequired, err error,
) {
	if len(events) == 0 {
		return nil, nil, nil, nil
	}
	// Validate before touching the pool: caller-side bugs surface even
	// when the pool is misconfigured.
	if vErr := validator.ValidateBatch(events, time.Now().UTC(), nil); vErr != nil {
		return nil, nil, nil, fmt.Errorf("PushEvents validate: %w", vErr)
	}
	if p == nil || p.p == nil {
		return nil, nil, nil, fmt.Errorf("PushEvents: pool not open")
	}

	cfg := DefaultRetryConfig()
	err = retryWithBackoff(ctx, cfg, func(ctx context.Context) error {
		ids, conf, ren, opErr := p.pushEventsOnce(ctx, events)
		if opErr != nil {
			return opErr
		}
		acceptedIDs = ids
		conflicts = conf
		renumbers = ren
		return nil
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("PushEvents: %w", err)
	}
	return acceptedIDs, conflicts, renumbers, nil
}

// pushEventsOnce runs one PG transaction's worth of pushes. Wrapped
// by retryWithBackoff in PushEvents.
func (p *Pool) pushEventsOnce(ctx context.Context, events []*model.SyncEvent) (
	[]string, []ConflictDescriptor, []RenumberRequired, error,
) {
	tx, err := p.p.Begin(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	acc := &pushAccum{
		seenPrefixes: make(map[string]struct{}, 1),
		// Track create_node numbers claimed earlier in THIS batch. The
		// partial unique index only sees committed rows, so two creates for
		// the same number within one push must be serialized here: the first
		// claims the key, later ones resolve against it — same uid is a no-op,
		// a distinct uid renumbers (ADR-003 §6.1/F-1).
		batchClaims: make(map[registryKey]batchClaim, len(events)),
	}

	for _, e := range events {
		if err := p.pushOneEvent(ctx, tx, e, acc); err != nil {
			return nil, nil, nil, err
		}
	}

	// Record the calling CLI's version for each project touched, in the
	// same tx, so the gate's view reflects this push atomically. No-op
	// when no client identity is set (SetClientIdentity not called).
	for prefix := range acc.seenPrefixes {
		if err := p.recordClientOnPush(ctx, tx, prefix); err != nil {
			return nil, nil, nil, fmt.Errorf("record client for %s: %w", prefix, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("commit: %w", err)
	}
	return acc.accepted, acc.conflicts, acc.renumbers, nil
}

// pushAccum accumulates one push transaction's outcomes across events so
// pushEventsOnce stays a thin loop and per-event handling lives in
// pushOneEvent. seenPrefixes and batchClaims are the cross-event state
// (version-gate projects and intra-batch number claims, ADR-003 §6.1/F-1).
type pushAccum struct {
	accepted     []string
	conflicts    []ConflictDescriptor
	renumbers    []RenumberRequired
	seenPrefixes map[string]struct{}
	batchClaims  map[registryKey]batchClaim
}

// pushOneEvent runs the registry check, conflict detection, and insert for a
// single event, folding the results into acc. It short-circuits a create
// that the registry resolves to a renumber or a no-op (ADR-003 §6/§9) before
// any insert.
func (p *Pool) pushOneEvent(ctx context.Context, tx pgx.Tx, e *model.SyncEvent, acc *pushAccum) error {
	// Registry check (ADR-003 §6/§9), keyed on the node's stable uid
	// (ADR-003 §2): a create_node for an already-held (project,
	// display_path) is either the SAME logical node (a no-op — e.g. a
	// --force re-backfill, MTIX-30.15) or a DIFFERENT one (renumber-
	// required; first-writer-wins, no node lost). The zero outcome means
	// the number is free and we insert it below.
	outcome, err := registryDecide(ctx, tx, e, acc.batchClaims)
	if err != nil {
		return fmt.Errorf("registry check for %s: %w", e.EventID, err)
	}
	if outcome.renumber != nil {
		acc.renumbers = append(acc.renumbers, *outcome.renumber)
		acc.seenPrefixes[e.ProjectPrefix] = struct{}{}
		return nil
	}
	if outcome.noop {
		// Same logical node re-pushed (stable uid): nothing to insert and
		// NOT a renumber. The registry already holds this node; the partial
		// UNIQUE index would reject a second create_node row anyway, so we
		// short-circuit before the insert (MTIX-30.15).
		//
		// It IS reported accepted so the pusher marks it pushed and stops
		// re-sending — the hub has absorbed it. Without this, a --force
		// re-backfill would loop forever re-pushing a never-acknowledged
		// event (the hang this ticket fixes).
		acc.accepted = append(acc.accepted, e.EventID)
		acc.seenPrefixes[e.ProjectPrefix] = struct{}{}
		return nil
	}

	conf, err := detectConflicts(ctx, tx, e)
	if err != nil {
		return fmt.Errorf("detect conflicts for %s: %w", e.EventID, err)
	}
	acc.conflicts = append(acc.conflicts, conf...)

	vcJSON, err := json.Marshal(e.VectorClock)
	if err != nil {
		return fmt.Errorf("marshal VC for %s: %w", e.EventID, err)
	}
	// uid is dual-carried alongside node_id (ADR-003 §3, §7 Phase 3).
	// NULL when the pusher (an old CLI) does not set it; apply then falls
	// back to node_id. NULLIF keeps the omitempty contract on the hub: an
	// empty string lands as SQL NULL, so the column matches the wire shape
	// and pulls back empty.
	tag, err := tx.Exec(ctx, `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (event_id) DO NOTHING`,
		e.EventID, e.ProjectPrefix, e.NodeID, e.UID, string(e.OpType), string(e.Payload),
		e.WallClockTS, e.LamportClock, string(vcJSON),
		e.AuthorID, e.AuthorMachineHash,
	)
	if err != nil {
		return fmt.Errorf("insert %s: %w", e.EventID, err)
	}
	if tag.RowsAffected() == 1 {
		acc.accepted = append(acc.accepted, e.EventID)
	}
	acc.seenPrefixes[e.ProjectPrefix] = struct{}{}

	return persistConflicts(ctx, tx, conf)
}

// persistConflicts writes each detected field-level conflict to the
// hub's sync_conflicts table inside the push transaction (MTIX-15.5.2).
// Resolution is recorded as 'lww'; a manual override via mtix sync
// conflicts resolve (15.7) INSERTs a new resolution='manual' row that
// supersedes the lww row, because the 006_triggers.sql trigger forbids
// UPDATE on sync_conflicts. Parameterized SQL only.
func persistConflicts(ctx context.Context, tx pgx.Tx, conf []ConflictDescriptor) error {
	for _, c := range conf {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sync_conflicts
			  (event_id_a, event_id_b, node_id, field_name, resolution)
			VALUES ($1, $2, $3, $4, 'lww')`,
			c.NewEventID, c.ConflictingEventID, c.NodeID, c.FieldName,
		); err != nil {
			return fmt.Errorf("persist conflict %s vs %s: %w",
				c.NewEventID, c.ConflictingEventID, err)
		}
	}
	return nil
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
		SELECT event_id, project_prefix, node_id, uid, op_type, payload,
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
		var uid *string // NULL for uid-less (old-CLI) events (ADR-003 §7 Phase 3)
		var createdAt time.Time
		if err := rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &uid, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash, &createdAt,
		); err != nil {
			return nil, false, err
		}
		if uid != nil {
			e.UID = *uid
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
