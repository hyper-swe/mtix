// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"errors"

	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// segmentTurn is one segment to ingest: the file, whether it is sealed, the
// persisted position that decides which of its records apply, and the walk
// that decides whether a hole precedes it. next is the segment after it, nil
// for the newest, which is what can explain a torn tail (FR-21 5.5).
type segmentTurn struct {
	file   segment.File
	next   *segment.File
	sealed bool
	prev   Position
	ch     chain
}

// ingestSegment reads and applies one segment, returning what the scan
// reached and whether the peer's stream stops here.
func (in *Ingestor) ingestSegment(ctx context.Context, peerID string, t segmentTurn, stats *Stats) (Outcome, bool) {
	file := t.file
	raw, header, key, err := in.openSegment(peerID, t)
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}
	// The scan is strict: damage inside a sealed segment is never excused.
	// Only a verdict that is a torn tail goes on to finishTornSeal, which
	// decides whether the successor explains it (FR-21 5.4, 5.5).
	sc, err := segment.NewScanner(bytes.NewReader(raw), segment.ScanOptions{
		Sealed: t.sealed, Key: key, ExpectPeerID: peerID,
	})
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}
	out, stop := in.applyPrefix(ctx, peerID, sc, t.prev, stats)
	if stop {
		return out, true
	}
	out.Header = header
	out.Truncated = sc.Truncated()
	out.Verdict = sc.Err()
	if t.sealed && segment.IsIncomplete(out.Verdict) {
		in.finishTornSeal(peerID, raw, t, sc, &out)
	}
	if out.Verdict == nil {
		return out, false
	}
	// A MAC failure is a corruption-class verdict like any other — the
	// reader stalls on it per §5.4 — and it ALSO increments the §8.2
	// counter, because a spike for one peer means something that is not
	// that peer is writing its directory. One condition, a verdict and a
	// signal.
	if errors.Is(out.Verdict, segment.ErrMACMismatch) {
		stats.AuthFailures++
	}
	in.stall(stats, peerID, file.No, segment.CodeOf(out.Verdict), out.Verdict)
	return out, true
}

// applyPrefix applies the records of one scan that are ahead of prev and
// returns where the scan reached. It reports true when a policy stall stops
// the stream.
func (in *Ingestor) applyPrefix(ctx context.Context, peerID string, sc *segment.Scanner, prev Position, stats *Stats) (Outcome, bool) {
	header := sc.Header()
	out := Outcome{Header: header}
	applied := segment.Cursor{SegmentNo: header.SegmentNo, RS: prev.RS, PubEpoch: header.PubEpoch}
	if header.PubEpoch != prev.PubEpoch {
		// A new publisher epoch restarts relay sequences at a declared
		// base, so the previous epoch's position says nothing about
		// where this segment begins.
		applied.RS = header.FirstRS - 1
	}
	for sc.Next() {
		rec := sc.Record()
		if rec.RS <= applied.RS {
			// Already applied in an earlier poll; the scan re-reads a
			// segment from its start when resuming inside it.
			continue
		}
		if stop := in.applyRecord(ctx, peerID, header, rec, stats); stop {
			// A policy stall banks what was APPLIED, not what was
			// delivered: the refused record itself must stay ahead of
			// the cursor so the next poll meets it again and the stall
			// is durable rather than silently stepped over.
			out.Reached = applied
			return out, true
		}
		applied.RS = rec.RS
	}
	out.Reached = applied
	return out, false
}
