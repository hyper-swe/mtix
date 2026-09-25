// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Held push events of `mtix sync push` (MTIX-95.12, review F-15).
//
// The hub refuses a whole PushEvents batch when any event in it is invalid
// (FR-18.7). An event that is legal locally but invalid on the wire, above
// all one whose payload is over the 64 KB cap (a prompt may be 100 KB),
// therefore failed every batch, and because the queue head never changed
// the replica stopped pushing for good. Push now validates each pending
// event first, with the rules the hub applies (transport.ValidatePushEvent),
// and holds an invalid one in the local sync_quarantine table with source
// push and the reason; the rest of the batch is pushed. A held event keeps
// its sync_events row, still pending, and readPendingBatch leaves it out,
// so it cannot block the queue.
//
// Three kinds of hold, told apart by the reason's prefix:
//   - permanent ("too large: ...", "refused: ..."): the event breaks a size,
//     depth, grammar or cap rule, which never changes. It stays held.
//   - temporary ("temporary: clock: ..."): the event is stamped more than 24h
//     ahead of this machine's clock. Every push checks it again and releases
//     it once it passes (releasePushHolds).
//   - dependent ("depends on held create of <event id> (<node>)"): the event
//     is about a task in the subtree of a held task creation (the subtree
//     rule, sync_push_hold_rule.go). When the creation's hold ends it is
//     validated and released with the creation, or, if the hub would refuse
//     it, kept under its own reason; under a permanent hold it stays held.

const (
	// holdTooLargePrefix starts the reason of a payload over the wire cap.
	holdTooLargePrefix = "too large: "
	// holdRefusedPrefix starts the reason of any other permanent refusal.
	holdRefusedPrefix = "refused: "
	// holdClockPrefix starts the reason of a temporary, clock-based hold.
	holdClockPrefix = "temporary: clock: "
	// holdDependsPrefix starts the reason of an event held because it
	// depends on a held task creation.
	holdDependsPrefix = "depends on held create of "
)

// eventPusher is the hub surface pushLoop sends batches through;
// *transport.Pool implements it. Tests pass a fake hub (MTIX-95.12).
type eventPusher interface {
	PushEventsWithRenumbers(ctx context.Context, events []*model.SyncEvent) (
		acceptedIDs []string, conflicts []transport.ConflictDescriptor,
		renumbers []transport.RenumberRequired, err error)
}

// batchHolds is what push decided for one pending batch: the events to
// send, in order, and the holds to record.
type batchHolds struct {
	valid []*model.SyncEvent
	holds []sqlite.QuarantinedEvent
}

// decideBatch decides, for each event of a pending batch in Lamport order,
// whether push holds it: because a held creation in blocked blocks it (the
// subtree rule, checked first), or because the hub would refuse it
// (transport.ValidatePushEvent at the store's clock). A creation it holds
// joins blocked, so the rest of its subtree waits for it in this and every
// later batch. The tasks the events are about are read, and the rule
// applied, only when it can hold something: some creation is held, or this
// batch has a refused one. It writes nothing (heldIndex.holdBatch records
// the holds once the decision stands).
func decideBatch(ctx context.Context, store *sqlite.Store, events []*model.SyncEvent, blocked *blockers,
) (batchHolds, error) {
	now := store.Now()
	refusals := make([]error, len(events))
	rule := !blocked.empty()
	for i, e := range events {
		refusals[i] = transport.ValidatePushEvent(e, now)
		rule = rule || (refusals[i] != nil && e.OpType == model.OpCreateNode)
	}
	checks, err := batchChecks(ctx, store, events, rule)
	if err != nil {
		return batchHolds{}, err
	}
	var d batchHolds
	for i, e := range events {
		reason, hold := pushHoldDecision(e, checkAt(checks, i), refusals[i], blocked)
		if !hold {
			d.valid = append(d.valid, e)
			continue
		}
		if c := checkAt(checks, i); c != nil && e.OpType == model.OpCreateNode {
			blocked.add(&blocker{eventID: e.EventID, nodeID: e.NodeID, uid: c.subject.UID, lamport: e.LamportClock},
				c.subject.CurrentNodeID)
		}
		q, err := pushHold(e, reason)
		if err != nil {
			return batchHolds{}, err
		}
		d.holds = append(d.holds, q)
	}
	return d, nil
}

// recordBatch records the holds of d in sync_quarantine with source push
// and the reason, names each on stderr, and returns the events to send and
// how many it newly held.
func recordBatch(ctx context.Context, stderr io.Writer, store *sqlite.Store, d batchHolds,
) ([]*model.SyncEvent, int, error) {
	if len(d.holds) == 0 {
		return d.valid, 0, nil
	}
	held, err := recordPushHolds(ctx, store, d.holds)
	if err != nil {
		return nil, 0, err
	}
	for _, h := range d.holds {
		fmt.Fprintf(stderr, "push: held event %s (%s %s), not pushed: %s. See mtix sync doctor\n",
			oneLine(h.EventID), oneLine(h.NodeID), oneLine(h.OpType), h.Reason)
	}
	return d.valid, held, nil
}

// pushSubjectsHookKey is the context key of a test seam (MTIX-95.12): a
// func() under it runs just before a batch's tasks are read, so a test can
// commit a change from another connection at exactly that point. Push
// never sets it.
type pushSubjectsHookKey struct{}

// batchChecks returns the holdCheck of each of events, with the task each
// is about (sqlite.PushSubjects), when rule is set; with rule unset it
// returns nil and reads nothing.
func batchChecks(ctx context.Context, store *sqlite.Store, events []*model.SyncEvent, rule bool) ([]holdCheck, error) {
	if !rule {
		return nil, nil
	}
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.EventID
	}
	if hook, ok := ctx.Value(pushSubjectsHookKey{}).(func()); ok {
		hook()
	}
	subjects, err := store.PushSubjects(ctx, ids)
	if err != nil {
		return nil, err
	}
	checks := make([]holdCheck, len(events))
	for i, e := range events {
		checks[i] = holdCheck{eventID: e.EventID, nodeID: e.NodeID, op: e.OpType,
			lamport: e.LamportClock, subject: subjects[e.EventID]}
	}
	return checks, nil
}

// checkAt returns the i-th of checks, or nil when checks is nil.
func checkAt(checks []holdCheck, i int) *holdCheck {
	if checks == nil {
		return nil
	}
	return &checks[i]
}

// pushHoldDecision decides whether push holds e (MTIX-95.12). The subtree
// rule comes first: an event that a held creation in blocked blocks is held
// with that creation's reason, whatever else is wrong with it (c is nil
// when no creation can block). Otherwise refused, the hub's refusal of e,
// holds it with its own reason.
func pushHoldDecision(e *model.SyncEvent, c *holdCheck, refused error, blocked *blockers) (reason string, hold bool) {
	if c != nil {
		if _, why, ok := blocked.holdFor(*c); ok {
			return why, true
		}
	}
	if refused == nil {
		return "", false
	}
	return pushHoldReason(e, refused), true
}

// pushHold is the sync_quarantine row that holds e for reason.
func pushHold(e *model.SyncEvent, reason string) (sqlite.QuarantinedEvent, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return sqlite.QuarantinedEvent{}, fmt.Errorf("hold %s: encode event: %w", e.EventID, err)
	}
	return sqlite.QuarantinedEvent{EventID: e.EventID, RawEvent: string(raw),
		Reason: reason, NodeID: e.NodeID, OpType: string(e.OpType)}, nil
}

// recordPushHolds stores holds and returns how many of them were not held
// before; an event already held only has one more attempt counted.
func recordPushHolds(ctx context.Context, store *sqlite.Store, holds []sqlite.QuarantinedEvent) (int, error) {
	before, err := store.CountHeldPushEvents(ctx)
	if err != nil {
		return 0, err
	}
	if holdErr := store.HoldPushEvents(ctx, holds, version); holdErr != nil {
		return 0, holdErr
	}
	after, err := store.CountHeldPushEvents(ctx)
	if err != nil {
		return 0, err
	}
	return after - before, nil
}

// pushHoldReason renders why the hub would refuse e (MTIX-95.12), with the
// prefix of its kind: temporary for the FR-18.8 future-stamp rule, which
// depends on the clock; permanent otherwise, naming for a payload over the
// wire cap its size, the limit and the field that makes up most of it. One
// line, at most maxQuarantineReason runes.
func pushHoldReason(e *model.SyncEvent, refused error) string {
	switch {
	case errors.Is(refused, validator.ErrTimestampFuture):
		refused = fmt.Errorf("%s%w", holdClockPrefix, refused)
	case errors.Is(refused, validator.ErrPayloadTooLarge):
		refused = fmt.Errorf("%spayload %d bytes, over the %d-byte sync limit; the %s field is largest",
			holdTooLargePrefix, len(e.Payload), validator.MaxPayloadBytes, validator.LargestPayloadField(e.Payload))
	default:
		refused = fmt.Errorf("%s%w", holdRefusedPrefix, refused)
	}
	return quarantineReason(refused)
}

// printHeldPushEvents reports on stdout, at the end of a push, how many
// events push holds, if any, and where to see them.
func printHeldPushEvents(ctx context.Context, stdout, stderr io.Writer, store *sqlite.Store) {
	held, err := store.CountHeldPushEvents(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "mtix sync push: %s\n", err)
		return
	}
	if held > 0 {
		fmt.Fprintf(stdout,
			"held: %d events not pushed because the hub would refuse them or they depend on a held task creation (see mtix sync doctor)\n",
			held)
	}
}
