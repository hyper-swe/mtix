// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// tornSeal is a sealed segment whose scan stopped at an unfinished tail, with
// what is needed to ask whether its successor explains the tail.
type tornSeal struct {
	peerID string
	header segment.Header
	// prefixRS is the relay sequence of the last whole record in the file.
	prefixRS uint64
	// tail is every byte after the last whole record of the sealed file.
	tail   []byte
	next   *segment.File
	keyFor func(segment.Header) ([]byte, error)
}

// explain applies FR-21 5.5: a torn sealed tail is excused only when the very
// next segment of the same peer and epoch resumes at or before the position
// after the last whole record AND its first record verifies. The first record
// is read here, with its MAC, so a successor that is only a header, or whose
// header was edited, never excuses anything. It returns the successor header.
func (ts tornSeal) explain() (segment.Header, bool) {
	if ts.next == nil || !ts.tailIsFinalFrame() {
		return segment.Header{}, false
	}
	raw, err := readSegment(ts.next.Path)
	if err != nil {
		return segment.Header{}, false
	}
	succ, err := segment.ParseHeader(raw)
	if err != nil {
		return segment.Header{}, false
	}
	sealed := segment.Result{
		Header: ts.header, Truncated: true,
		Cursor: segment.Cursor{SegmentNo: ts.header.SegmentNo, RS: ts.prefixRS, PubEpoch: ts.header.PubEpoch},
	}
	if !segment.ExplainsTornSeal(sealed, succ) {
		return segment.Header{}, false
	}
	key, err := ts.keyFor(succ)
	if err != nil {
		return segment.Header{}, false
	}
	sc, err := segment.NewScanner(bytes.NewReader(raw), segment.ScanOptions{
		Key: key, ExpectPeerID: ts.peerID,
	})
	if err != nil {
		return segment.Header{}, false
	}
	return succ, sc.Next()
}

// tailIsFinalFrame is true when the damage starts at the final frame boundary:
// the tail is the incomplete end of one frame with no verifiable record after
// it. A bad length field in the middle of the file fails this (FR-21 5.5).
func (ts tornSeal) tailIsFinalFrame() bool {
	key, err := ts.keyFor(ts.header)
	if err != nil {
		return false
	}
	return segment.TailIsOneFrame(ts.tail, ts.header, key)
}

// newTornSeal collects what explain needs from a finished sealed scan of raw.
func newTornSeal(peerID string, raw []byte, sc *segment.Scanner, next *segment.File, keyFor func(segment.Header) ([]byte, error)) tornSeal {
	return tornSeal{peerID: peerID, header: sc.Header(), prefixRS: sc.Cursor().RS,
		tail: raw[sc.Offset():], next: next, keyFor: keyFor}
}

// finishTornSeal settles a sealed segment that ended in a torn tail: explained,
// the outcome carries no verdict and the walk may continue into the successor;
// otherwise it is RELAY_SEGMENT_CORRUPT as FR-21 5.4 demands (MTIX-288).
func (in *Ingestor) finishTornSeal(peerID string, raw []byte, t segmentTurn, sc *segment.Scanner, out *Outcome) {
	ts := newTornSeal(peerID, raw, sc, t.next, in.keyFor)
	succ, ok := ts.explain()
	if !ok {
		return // the strict verdict stands: RELAY_SEGMENT_CORRUPT
	}
	out.Verdict = nil
	out.Reconciled, out.ResumeRS = true, succ.FirstRS-1
	in.log.Warn("reconciled torn seal",
		slog.String("peer", peerID), slog.Uint64("segment", ts.header.SegmentNo),
		slog.Uint64("successor", succ.SegmentNo))
}

// advanceTorn moves the walk across an explained torn seal. The successor may
// begin below what was applied (a publisher re-emitting from its durable
// cursor), so the walk is placed just before its first record.
func (c *chain) advanceTorn(reached Position, resumeRS uint64) {
	if resumeRS < reached.RS {
		reached.RS = resumeRS
	}
	c.advance(reached)
}

// step moves the walk past a segment that read cleanly, crossing an explained
// torn seal where the outcome says so.
func (c *chain) step(out Outcome) {
	reached := Position{SegmentNo: out.Reached.SegmentNo, RS: out.Reached.RS, PubEpoch: out.Reached.PubEpoch}
	if out.Reconciled {
		c.advanceTorn(reached, out.ResumeRS)
		return
	}
	c.advance(reached)
}
