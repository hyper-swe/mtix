// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "time"

// Event times at apply (MTIX-95.26; FR-18.9).
//
// A synced event carries its author's wall clock as wall_clock_ts, in Unix
// milliseconds. The apply code stores timestamps derived from it: a comment's
// annotation created_at and the node's updated_at (applyComment), and a
// terminal closed_at (applyTransitionStatus through the workflow winner
// table). Each of them converts wall_clock_ts with eventTime and nothing
// else, so every stored value is one the store reads back. Elsewhere apply
// uses wall_clock_ts only as an integer: stored in the sync_events log and
// compared as the field LWW tie-break (detectLWWOutcome).

// Stored timestamps are RFC3339 text or JSON time values, both of which
// carry four-digit years, so an event time is used only within these years.
const (
	minEventYear = 1
	maxEventYear = 9999
)

// eventTime returns the time a synced event's wall_clock_ts names, in UTC, for
// a timestamp the apply code stores (MTIX-95.26). Within years 1..9999 it is
// the event's own instant, to the millisecond, so every replica that applies
// the event stores the same value. Outside that range it is applyTime, in
// UTC: the time this replica applies the event, the value apply stamps when
// an event carries no usable time. The stored timestamp therefore always lies
// in the range every reader of the store parses.
//
// Residual until envelope validation bounds wall_clock_ts at ingest
// (MTIX-95.11): for an event whose time lies outside the range, each replica
// stores its own apply time, so a comment's created_at, its position among
// the node's annotations and the node's updated_at, or a terminal closed_at,
// can differ between replicas.
func eventTime(wallClockMS int64, applyTime time.Time) time.Time {
	t := time.UnixMilli(wallClockMS).UTC()
	if y := t.Year(); y < minEventYear || y > maxEventYear {
		return applyTime.UTC()
	}
	return t
}
