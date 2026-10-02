// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import "time"

// Stored times at apply (MTIX-95.26; FR-18.9).
//
// A synced event carries its author's wall clock as wall_clock_ts, in Unix
// milliseconds. The apply code stores timestamps derived from it: a comment's
// annotation created_at and the node's updated_at (applyComment), and a
// terminal closed_at (applyTransitionStatus through the workflow winner
// table). Each of them converts wall_clock_ts with eventTime and nothing
// else. Elsewhere apply uses wall_clock_ts only as an integer: stored in the
// sync_events log and compared as the field LWW tie-break (detectLWWOutcome).
//
// The one time a payload carries, a defer's until, becomes defer_until only
// through storedDeferUntil (defer.go, MTIX-95.22; resolveWorkflowWrite), which
// stores UTC text within years 1..9999 and NULL otherwise. Every stored value
// is therefore one the store reads back.

// Stored timestamps are RFC3339 text or JSON time values, both of which
// carry four-digit years, so a time is stored only within these UTC years.
const (
	minStoredYear = 1
	maxStoredYear = 9999
)

// inStoredYearRange reports whether t's UTC year lies within
// minStoredYear..maxStoredYear (MTIX-95.26).
func inStoredYearRange(t time.Time) bool {
	y := t.UTC().Year()
	return y >= minStoredYear && y <= maxStoredYear
}

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
	if !inStoredYearRange(t) {
		return applyTime.UTC()
	}
	return t
}
