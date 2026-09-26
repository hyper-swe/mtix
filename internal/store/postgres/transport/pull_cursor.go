// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import "github.com/hyper-swe/mtix/internal/model"

// PullCursor is a keyset position in the hub event log, in the order
// PullEvents serves it: (lamport_clock, event_id) (MTIX-95.4; ADR-006 D6,
// D16). It is the Lamport clock and the event id of the last event pulled.
//
// The event id is the tiebreak. Lamport clocks are not unique, so a
// Lamport-only cursor skipped the events that share the last pulled clock
// but did not fit on the page.
//
// An empty EventID sorts before every event id, so PullCursor{Lamport: L}
// selects every event at L and above. That is the position of a client
// upgraded from a Lamport-only cursor: it pulls the events at exactly L
// again, and applies them idempotently (applied_events dedupes them). The
// zero value selects the whole log.
type PullCursor struct {
	Lamport int64
	EventID string
}

// CursorAt returns the keyset position of e: the cursor after which the
// next page starts when e is the last event of a page (MTIX-95.4).
func CursorAt(e *model.SyncEvent) PullCursor {
	return PullCursor{Lamport: e.LamportClock, EventID: e.EventID}
}
