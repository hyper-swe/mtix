// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Push acknowledgement by presence (MTIX-95.3, ADR-006 D7/D48, review F-80).
//
// The hub used to acknowledge an event only when its INSERT ... ON CONFLICT
// (event_id) DO NOTHING inserted a row. An event that reached the hub but
// was never marked pushed locally (a lost commit acknowledgement, a crash or
// disk failure before the local mark, the transport's own retry after a
// commit error, which runs the whole transaction again) was then never
// acknowledged, stayed pending forever, and a full batch of such events at
// the head of the queue stopped every later push. Push now reads, with one
// query per batch, the hub copy of each event whose INSERT inserted no row,
// and acknowledges it when the copy has the same node, op and payload. A
// copy with other content is an integrity problem, never an acknowledgement
// (ADR-006 I6): it is reported as a PresenceMismatch.

// PushResult is what the hub did with one pushed batch (MTIX-95.3). The
// events in Inserted and AlreadyPresent are acknowledged: the pusher marks
// both pushed. Every other outcome leaves the event unacknowledged.
type PushResult struct {
	// Inserted lists, in batch order, the events this push wrote to the hub.
	Inserted []string
	// AlreadyPresent lists the events the hub already held: an event whose
	// id is on the hub with the same node, op and payload, and a creation
	// of a task the hub already holds under the same uid (the same-node
	// no-op, MTIX-30.15).
	AlreadyPresent []string
	// Conflicts, Renumbers and Collisions are the outcomes
	// PushEventsWithCollisions reports.
	Conflicts  []ConflictDescriptor
	Renumbers  []RenumberRequired
	Collisions []RestoreCollision
	// Mismatches lists the events whose id the hub already holds with a
	// different node, op or payload. They are not acknowledged: the hub
	// keeps its copy, and the pusher must surface the event and never mark
	// it pushed.
	Mismatches []PresenceMismatch
}

// Accepted returns the events the hub now holds from the batch, the ones
// the pusher marks pushed: Inserted, then AlreadyPresent (MTIX-95.3).
func (r PushResult) Accepted() []string {
	if len(r.Inserted)+len(r.AlreadyPresent) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Inserted)+len(r.AlreadyPresent))
	out = append(out, r.Inserted...)
	return append(out, r.AlreadyPresent...)
}

// PresenceMismatch is a pushed event whose id the hub already holds with
// other content (MTIX-95.3). Fields names what differs, in the order
// node_id, op_type, payload; HubNodeID and HubOpType are the hub copy's.
type PresenceMismatch struct {
	EventID   string   `json:"event_id"`
	Fields    []string `json:"fields"`
	HubNodeID string   `json:"hub_node_id"`
	HubOpType string   `json:"hub_op_type"`
}

// PushEventsResult validates the batch and pushes it in one PG transaction
// under the retry envelope, and reports what the hub did with each event
// (MTIX-95.3, ADR-006 D7): the events it inserted, the events it already
// held (acknowledged too, so a lost commit acknowledgement, a crash before
// the local mark or a retry after a network drop cannot leave an event
// pending forever), the events whose id it holds with other content (never
// acknowledged), and the conflict, renumber and restore-collision outcomes
// of PushEventsWithCollisions. Validation, atomicity and idempotency match
// PushEvents.
func (p *Pool) PushEventsResult(ctx context.Context, events []*model.SyncEvent) (PushResult, error) {
	if len(events) == 0 {
		return PushResult{}, nil
	}
	// Validate before touching the pool: caller-side bugs surface even
	// when the pool is misconfigured.
	if vErr := validator.ValidateBatch(events, p.now(), nil); vErr != nil {
		return PushResult{}, fmt.Errorf("PushEvents validate: %w", vErr)
	}
	if p == nil || p.p == nil {
		return PushResult{}, fmt.Errorf("PushEvents: pool not open")
	}

	var res PushResult
	cfg := DefaultRetryConfig()
	err := retryWithBackoff(ctx, cfg, func(ctx context.Context) error {
		once, opErr := p.pushEventsOnce(ctx, events)
		if opErr != nil {
			return opErr
		}
		res = once
		return nil
	})
	if err != nil {
		return PushResult{}, fmt.Errorf("PushEvents: %w", err)
	}
	return res, nil
}

// result returns the outcomes acc collected as a PushResult.
func (acc *pushAccum) result() PushResult {
	return PushResult{
		Inserted: acc.inserted, AlreadyPresent: acc.present,
		Conflicts: acc.conflicts, Renumbers: acc.renumbers, Collisions: acc.collisions,
		Mismatches: acc.mismatches,
	}
}

// commitPush commits the push transaction tx. The afterCommit test seam,
// when set, runs once the COMMIT succeeded and its error stands in for the
// commit's, as a lost commit acknowledgement would (MTIX-95.3, review F-80).
func (p *Pool) commitPush(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if p.afterCommit != nil {
		return p.afterCommit()
	}
	return nil
}

// presenceSQL reads, in one query per batch, the hub copy of each event of
// the batch whose INSERT inserted no row (MTIX-95.3). $1 is those events'
// ids and $2 their payloads, in the same order; the query is bounded by the
// primary key (event_id = ANY($1)). It returns, for each event the hub
// holds, its 1-based position in $1, the hub copy's node_id and op_type,
// and whether the hub copy's payload equals the pushed one as a JSON value:
// the hub stores payload as JSONB, which drops insignificant whitespace and
// orders keys, so the bytes cannot be compared. The pushed payload always
// parses here: the INSERT that found the id taken parsed it first.
const presenceSQL = `
SELECT p.n, h.node_id, h.op_type, h.payload = p.payload::jsonb
FROM unnest($1::text[], $2::text[]) WITH ORDINALITY AS p(event_id, payload, n)
JOIN sync_events AS h ON h.event_id = p.event_id
WHERE h.event_id = ANY($1)`

// hubCopy is what presenceSQL returns for one not-inserted event: the hub
// copy's node and op, and whether its payload equals the pushed one.
type hubCopy struct {
	nodeID, opType string
	samePayload    bool
}

// ackPresent resolves the events of acc.notInserted, whose INSERT ... ON
// CONFLICT (event_id) DO NOTHING inserted no row because the event_id is
// already on the hub (MTIX-95.3, ADR-006 D7/D48, review F-80). One
// presenceSQL query covers the whole batch. An event whose hub copy has the
// same node, op and payload joins acc.present, in batch order, so it is
// acknowledged and the pusher marks it pushed. One whose copy differs joins
// acc.mismatches and is never acknowledged (ADR-006 I6). An event the query
// does not return, which cannot happen while the hub log is append-only, is
// left unacknowledged.
func ackPresent(ctx context.Context, tx pgx.Tx, acc *pushAccum) error {
	if len(acc.notInserted) == 0 {
		return nil
	}
	copies, err := readHubCopies(ctx, tx, acc.notInserted)
	if err != nil {
		return fmt.Errorf("check %d events already on the hub: %w", len(acc.notInserted), err)
	}
	for i, e := range acc.notInserted {
		c, ok := copies[i]
		if !ok {
			continue
		}
		if fields := c.differences(e); len(fields) > 0 {
			acc.mismatches = append(acc.mismatches, PresenceMismatch{
				EventID: e.EventID, Fields: fields, HubNodeID: c.nodeID, HubOpType: c.opType,
			})
			continue
		}
		acc.present = append(acc.present, e.EventID)
	}
	return nil
}

// readHubCopies runs presenceSQL for events and returns the hub copy of
// each one the hub holds, keyed by its index in events.
func readHubCopies(ctx context.Context, tx pgx.Tx, events []*model.SyncEvent) (map[int]hubCopy, error) {
	ids := make([]string, len(events))
	payloads := make([]string, len(events))
	for i, e := range events {
		ids[i], payloads[i] = e.EventID, string(e.Payload)
	}
	rows, err := tx.Query(ctx, presenceSQL, ids, payloads)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	copies := make(map[int]hubCopy, len(events))
	for rows.Next() {
		var n int
		var c hubCopy
		if err := rows.Scan(&n, &c.nodeID, &c.opType, &c.samePayload); err != nil {
			return nil, err
		}
		copies[n-1] = c
	}
	return copies, rows.Err()
}

// differences names the fields in which the hub copy c differs from the
// pushed event e, in the order node_id, op_type, payload; none when c is
// the same event.
func (c hubCopy) differences(e *model.SyncEvent) []string {
	var fields []string
	if c.nodeID != e.NodeID {
		fields = append(fields, "node_id")
	}
	if c.opType != string(e.OpType) {
		fields = append(fields, "op_type")
	}
	if !c.samePayload {
		fields = append(fields, "payload")
	}
	return fields
}
