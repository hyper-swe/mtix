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
// that decides whether a hole precedes it.
type segmentTurn struct {
	file   segment.File
	sealed bool
	prev   Position
	ch     chain
}

// ingestSegment reads and applies one segment, returning what the scan
// reached and whether the peer's stream stops here.
func (in *Ingestor) ingestSegment(ctx context.Context, peerID string, t segmentTurn, stats *Stats) (Outcome, bool) {
	prev, file, sealed := t.prev, t.file, t.sealed
	raw, header, key, err := in.openSegment(peerID, t)
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}

	sc, err := segment.NewScanner(bytes.NewReader(raw), segment.ScanOptions{
		Sealed:       sealed,
		Key:          key,
		ExpectPeerID: peerID,
	})
	if err != nil {
		in.stall(stats, peerID, file.No, segment.CodeOf(err), err)
		return Outcome{Verdict: err}, true
	}

	out := Outcome{Header: header}
	applied := segment.Cursor{
		SegmentNo: header.SegmentNo, RS: prev.RS, PubEpoch: header.PubEpoch,
	}
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
	out.Truncated = sc.Truncated()
	out.Verdict = sc.Err()

	if out.Verdict != nil {
		// A MAC failure is a corruption-class verdict like any other —
		// the reader stalls on it per §5.4 — and it ALSO increments the
		// §8.2 counter, because a spike for one peer means something
		// that is not that peer is writing its directory. One condition,
		// a verdict and a signal.
		if errors.Is(out.Verdict, segment.ErrMACMismatch) {
			stats.AuthFailures++
		}
		in.stall(stats, peerID, file.No, segment.CodeOf(out.Verdict), out.Verdict)
		return out, true
	}
	return out, false
}
