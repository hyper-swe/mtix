// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package ingest holds the FR-21 reader-side cursor rules: given what a
// segment scan produced, where may a reader's durable position move?
//
// The rules live apart from the scanning and the applying because they
// are the part that is easy to get subtly wrong and expensive to get
// wrong at all. A cursor that advances one segment too far does not
// fail — it silently skips whatever the damage hid, and the fleet
// converges on a history missing a causal predecessor that no later
// check can reconstruct.
//
// The package is pure: standard library plus the frame types, no store
// imports, no goroutines.
package ingest

import (
	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// Position is a reader's durable position in one peer's stream, mirroring
// the columns the store persists (FR-21 §6.3).
type Position struct {
	SegmentNo uint64
	RS        uint64
	PubEpoch  uint16
}

// Outcome is what one segment scan produced.
type Outcome struct {
	// Header is the scanned segment's validated header.
	Header segment.Header

	// Delivered is how many records the scan handed to the apply path.
	Delivered int

	// Reached is the position after the last delivered record.
	Reached segment.Cursor

	// Truncated reports an active segment that stopped at an unfinished
	// tail — normal, and not a reason to withhold the prefix.
	Truncated bool

	// Verdict is the error that stopped the scan, or nil. A scan can
	// deliver records AND carry a verdict: a sealed segment hands over
	// its clean prefix and is then condemned.
	Verdict error
}

// Next returns the position a reader may persist after a scan, and
// whether it moved.
//
// The rule that matters: a scan carrying a verdict never moves the
// cursor, however many records it delivered. A sealed segment can hand
// over a clean prefix and then be condemned, and banking that prefix
// would step the reader over whatever the damage concealed — the exact
// silent hole FR-21 §5.4 refuses to allow. Those records were applied
// idempotently, so re-reading them on the next poll costs nothing,
// while the unmoved cursor keeps the stall visible in status until an
// operator repairs it.
//
// Otherwise the position moves to what the scan reached, subject to the
// same ordering the store enforces: within a publisher epoch it is a
// monotonic watermark that refuses replay from below, a higher epoch
// restarts relay sequences at a declared base so a lower rs under a
// newer epoch is genuine progress, and an older epoch is a rollback
// splice and is refused however high its rs.
func Next(prev Position, out Outcome) (Position, bool) {
	if out.Verdict != nil {
		return prev, false
	}
	next := Position{
		SegmentNo: out.Reached.SegmentNo,
		RS:        out.Reached.RS,
		PubEpoch:  out.Reached.PubEpoch,
	}
	if !advances(prev, next) {
		return prev, false
	}
	return next, true
}

// advances reports whether next is forward of prev under (PubEpoch, RS).
func advances(prev, next Position) bool {
	switch {
	case next.PubEpoch > prev.PubEpoch:
		return true
	case next.PubEpoch < prev.PubEpoch:
		return false
	default:
		return next.RS > prev.RS || (next.RS == prev.RS && next.SegmentNo > prev.SegmentNo)
	}
}

// Unread returns the segments a poll must open for one peer, given the
// reader's position.
//
// This is what makes the FR-21 §9 I/O bound a property rather than a
// hope: a tick lists a peer's directory once and opens only the
// segments at or after its cursor. The segment holding the cursor is
// included so a reader can resume inside it; everything below it is
// already applied and is never reopened, so the cost of a poll tracks
// new work and not the length of history.
//
// A publisher epoch bump does not change the rule. reset-peer
// republishes into FRESH segments with higher numbers, so the cursor's
// segment is still the floor.
//
// segs must be ordered by segment number, as the directory walker
// returns them.
func Unread(segs []segment.File, prev Position) []segment.File {
	for i, s := range segs {
		if s.No >= prev.SegmentNo {
			return segs[i:]
		}
	}
	return nil
}
