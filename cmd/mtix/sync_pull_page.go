// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// errPageNotAfterCursor marks a hub page that does not advance past the
// cursor it was asked for (MTIX-95.4). A keyset loop that followed it would
// ask the hub for the same pages forever.
var errPageNotAfterCursor = errors.New("hub page does not advance past the cursor")

// pageCursor is the keyset position a paging loop (pullLoop, cloneLoop,
// preflightClone) asks the hub for the next page after, with the event ids
// its pages have ended at, at that position's Lamport clock (MTIX-95.4).
type pageCursor struct {
	at transport.PullCursor
	// passed holds the event ids of at and of every earlier page end at
	// at.Lamport; it is reset when a page ends at a higher clock.
	passed map[string]struct{}
}

// newPageCursor starts a paging loop at start.
func newPageCursor(start transport.PullCursor) *pageCursor {
	return &pageCursor{at: start, passed: map[string]struct{}{start.EventID: {}}}
}

// advance moves the cursor to the last event of events, a non-empty page
// the hub returned for c.at (MTIX-95.4). It refuses a page that does not
// advance, with errPageNotAfterCursor naming the cursor and the page end:
// one that ends at a lower Lamport clock than the cursor, or at an event id
// the loop has already paged past at the cursor's clock (the cursor's own,
// or an earlier page end, which a hub serving alternating pages brings
// back). At one Lamport clock the hub orders event ids by its database
// collation, which this client does not know, so any other event id at
// that clock counts as progress: comparing ids byte by byte here could stop
// every pull on a hub whose collation orders some ids differently.
func (c *pageCursor) advance(events []*model.SyncEvent) error {
	end := transport.CursorAt(events[len(events)-1])
	switch {
	case end.Lamport > c.at.Lamport:
		c.passed = map[string]struct{}{}
	case end.Lamport == c.at.Lamport && !c.hasPassed(end.EventID):
	default:
		return fmt.Errorf("%w (cursor: lamport %d, event %q; page ends at lamport %d, event %q)",
			errPageNotAfterCursor, c.at.Lamport, c.at.EventID, end.Lamport, end.EventID)
	}
	c.passed[end.EventID] = struct{}{}
	c.at = end
	return nil
}

// hasPassed reports whether a page of this loop already ended at event id
// id at the cursor's Lamport clock, or the loop started there.
func (c *pageCursor) hasPassed(id string) bool {
	_, ok := c.passed[id]
	return ok
}
