// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// holdRelayCarried marks the reason of a held relay-carried creation, so
// doctor can give its fix (heldPushGuidance).
const holdRelayCarried = "carried by the relay"

// renumberOutcome is what resolveRenumbers did: creations renumbered locally,
// relay-carried creations held instead (relay), and the holds that were new.
type renumberOutcome struct{ local, relay, newlyHeld int }

// resolveRenumbers answers the hub's renumber-required outcomes of one batch
// and returns what it renumbered and held. A creation of this replica the relay never
// carried is renumbered (RenumberForHubRejection). A creation the relay carries,
// ingested from a relay peer or already published by this replica, is held
// instead (FR-21 §11.7): the relay has no durable change that moves its author
// to the hub's number, so renumbering here would leave the author, this
// gateway and the hub with different numbers for one uid. The hold is
// permanent, names the number and the relay origin, and `mtix sync doctor`
// reports it as a held push event.
func resolveRenumbers(ctx context.Context, stderr io.Writer, store *sqlite.Store,
	events []*model.SyncEvent, renumbers []transport.RenumberRequired, batch int,
) (renumberOutcome, error) {
	var out renumberOutcome
	byID := make(map[string]*model.SyncEvent, len(events))
	for _, e := range events {
		byID[e.EventID] = e
	}
	var d batchHolds
	for _, r := range renumbers {
		q, hold, err := resolveOneRenumber(ctx, store, byID[r.EventID], r.EventID, batch)
		if err != nil {
			return renumberOutcome{}, err
		}
		if hold {
			d.holds = append(d.holds, q)
		} else {
			out.local++
		}
	}
	out.relay = len(d.holds)
	if out.relay == 0 {
		return out, nil
	}
	_, held, err := recordBatch(ctx, stderr, store, d)
	out.newlyHeld = held
	return out, err
}

// resolveOneRenumber renumbers a creation of this replica, or returns the hold
// of a relay-origin one.
func resolveOneRenumber(ctx context.Context, store *sqlite.Store, e *model.SyncEvent, eventID string, batch int,
) (sqlite.QuarantinedEvent, bool, error) {
	relay, err := relayCarried(ctx, store, eventID)
	if err != nil {
		return sqlite.QuarantinedEvent{}, false, err
	}
	if !relay {
		if gap, ok := ctx.Value(renumberGapKey{}).(func()); ok {
			gap()
		}
		if err = renumberLocal(ctx, store, eventID, batch); !errors.Is(err, sqlite.ErrRelayPublishedCreate) {
			return sqlite.QuarantinedEvent{}, false, err
		}
		// A publisher framed the create after the check: hold it instead.
	}
	q, err := relayRenumberHold(e, eventID)
	return q, err == nil, err
}

// relayCarried reports whether the relay carries the creation: it came from a
// relay peer and is still pending for the hub, or this replica already
// published it. Only a creation the relay never carried may be renumbered here.
func relayCarried(ctx context.Context, store *sqlite.Store, eventID string) (bool, error) {
	forwarded, err := store.IsRelayForward(ctx, eventID)
	if err != nil || forwarded {
		return forwarded, err
	}
	return store.IsRelayPublished(ctx, eventID)
}

// renumberGapKey is the context key of a test seam: a func() under it runs
// between the published check and the renumber, where a test frames the
// create. Push never sets it.
type renumberGapKey struct{}

func renumberLocal(ctx context.Context, store *sqlite.Store, eventID string, batch int) error {
	if _, err := store.RenumberUnlessRelayPublished(ctx, eventID); err != nil {
		return fmt.Errorf("resolve renumber for %s (batch %d): %w", format.EventID(eventID), batch, err)
	}
	return nil
}

// relayRenumberHold is the hold row of a relay-origin creation the hub would
// renumber; e is the event the batch sent under eventID.
func relayRenumberHold(e *model.SyncEvent, eventID string) (sqlite.QuarantinedEvent, error) {
	if e == nil {
		return sqlite.QuarantinedEvent{}, fmt.Errorf("the hub asked to renumber event %s, which this batch did not send", format.EventID(eventID))
	}
	return pushHold(e, quarantineReason(fmt.Errorf(
		"%sthe hub holds %s for another task and this creation is "+holdRelayCarried+
			" (published to or ingested from the relay), which keeps its number; "+
			"renumbering it here would split the peers", holdRefusedPrefix, format.StripControlChars(e.NodeID))))
}
