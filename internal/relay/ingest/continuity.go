// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// chain is where one peer's stream has been read to, within one poll or one
// doctor check. Ingest and doctor both walk a peer's segments through it, so
// they judge every segment by the same rule and cannot disagree on a gap
// shape (MTIX-265).
//
// A cursor seeded by `relay clone` has segment 0 and a relay sequence: the
// snapshot covers every event up to that sequence, in the publisher epoch the
// snapshot stamped. While the walk has not gone past the clone position the
// chain stays seeded: a segment of that epoch needs only to reach back to the
// position (FirstRS <= RS+1), whatever lies below it, because the snapshot
// already holds those events. Relay sequences restart under a new publisher
// epoch (FR-21 5.7), so the floor is never compared across epochs: a segment of
// an older epoch lies inside the snapshot, and the first segment of a newer
// epoch starts a stream the snapshot never held and is judged by the epoch
// rules. A snapshot that did not stamp an epoch (epoch 0) is compared by
// relay sequence alone. Once a segment carries the stream past the position,
// the walk judges with full segment.CheckContinuity.
//
// lastNo is the highest segment number the walk has read, seeded or not. A
// seeded walk has no segment number of its own, so it uses lastNo to hold the
// newer epoch to the rule that it starts in the very next segment.
type chain struct {
	pos    Position
	seeded bool
	lastNo uint64
}

// newChain starts a walk at the persisted cursor. A snapshot may stamp a
// stream with a publisher epoch and relay sequence 0: the epoch has begun but
// the snapshot holds none of its records (a stream reset before its first
// publish), so older-epoch segments are still inside the snapshot. The chain
// only judges continuity: which records are applied is decided by the
// persisted position alone (MTIX-265).
func newChain(pos Position) chain {
	return chain{pos: pos, seeded: pos.SegmentNo == 0 && (pos.RS > 0 || pos.PubEpoch > 0)}
}

// sameStream reports whether a segment of epoch e is comparable by relay
// sequence with the seeded position.
func (c chain) sameStream(e uint16) bool {
	return c.pos.PubEpoch == 0 || c.pos.PubEpoch == e
}

// check reports a RELAY_GAP error when the segment described by h may not be
// read next: a segment deleted or never published leaves a hole here that the
// per-record scan inside one file cannot see (FR-21 5.3).
func (c chain) check(peerID string, h segment.Header) error {
	var err error
	switch {
	case !c.seeded:
		err = segment.CheckContinuity(segment.Cursor{
			SegmentNo: c.pos.SegmentNo, RS: c.pos.RS, PubEpoch: c.pos.PubEpoch,
		}, h)
	case c.sameStream(h.PubEpoch) && h.FirstRS > c.pos.RS+1:
		err = fmt.Errorf("%w: segment %d starts at rs %d, past the clone position %d",
			segment.ErrGap, h.SegmentNo, h.FirstRS, c.pos.RS)
	case h.PubEpoch > c.pos.PubEpoch && c.pos.PubEpoch > 0 && c.lastNo > 0 && h.SegmentNo != c.lastNo+1:
		// A newer epoch starts in the very next segment. The walk has read
		// segment lastNo, so a hole between them is a deleted segment.
		err = fmt.Errorf("%w: publisher epoch %d must arrive in segment %d, the one after %d, found %d",
			segment.ErrGap, h.PubEpoch, c.lastNo+1, c.lastNo, h.SegmentNo)
	}
	if err == nil {
		return nil
	}
	return fmt.Errorf("peer %s: %w; %s", peerID, err, gapRange(c.pos, h))
}

// advance records where a segment's scan reached. A seeded walk stays seeded
// until the stream passes the clone position within its epoch (or reaches a
// newer epoch), and its position never drops.
func (c *chain) advance(reached Position) {
	if reached.SegmentNo > c.lastNo {
		c.lastNo = reached.SegmentNo
	}
	if c.seeded && (reached.PubEpoch < c.pos.PubEpoch ||
		(c.sameStream(reached.PubEpoch) && reached.RS <= c.pos.RS)) {
		return
	}
	c.pos, c.seeded = reached, false
}

// openSegment reads and parses one segment, refuses it with RELAY_GAP when a
// hole precedes it (a deleted or never-published predecessor, MTIX-265), and
// resolves its key. An incomplete header (empty or short file) in a sealed
// segment is a corruption verdict, ErrSegmentCorrupt (RELAY_SEGMENT_CORRUPT),
// because a later segment proves it is final; in the active tail it is
// returned as incomplete so the caller retries on the next poll (FR-21 5.3,
// MTIX-288).
func (in *Ingestor) openSegment(peerID string, t segmentTurn) ([]byte, segment.Header, []byte, error) {
	raw, err := in.read(t.file.Path)
	if err != nil {
		return nil, segment.Header{}, nil, mediumErr(in.cfg.PeersDir, t.file.Path, err)
	}
	header, err := segment.ParseHeader(raw)
	if t.sealed && segment.IsIncomplete(err) {
		return nil, segment.Header{}, nil, fmt.Errorf("%w: sealed segment %d has only %d header bytes",
			segment.ErrSegmentCorrupt, t.file.No, len(raw))
	}
	if err != nil {
		return nil, segment.Header{}, nil, err
	}
	if gapErr := t.ch.check(peerID, header); gapErr != nil {
		return nil, segment.Header{}, nil, gapErr
	}
	key, err := in.keyFor(header)
	if err != nil {
		return nil, segment.Header{}, nil, err
	}
	return raw, header, key, nil
}

// gapRange renders the missing range for the operator.
func gapRange(chain Position, h segment.Header) string {
	if h.PubEpoch == chain.PubEpoch || chain.SegmentNo == 0 {
		if from := chain.RS + 1; h.FirstRS > from {
			return fmt.Sprintf("missing relay sequence %d through %d", from, h.FirstRS-1)
		}
	}
	return fmt.Sprintf("expected the segment after %d at relay sequence %d", chain.SegmentNo, chain.RS+1)
}

// Gap is one peer whose segments do not continue where this reader stands.
type Gap struct {
	PeerID string
	Reason string
}

// Unchecked is a peer whose walk stopped before its newest segment, so a hole
// beyond the stop is not ruled out.
type Unchecked struct {
	PeerID string
	Reason string
}

// GapReport is what the doctor's continuity walk found.
type GapReport struct {
	Gaps      []Gap
	Unchecked []Unchecked
}

// FindGaps is the read-only check behind doctor: it walks each peer's unread
// segments with the same chain ingest uses and reports every peer whose stream
// has a hole, whether a missing segment number or a jump in relay sequence. It
// scans records to learn where each segment ends, as ingest does, but applies
// nothing. keys resolves a frame epoch's key and is nil for an unauthenticated
// relay. A peer that cannot be walked to its newest segment (an unreadable
// file, a damaged header, a record that cannot be scanned, a key epoch this
// peer lacks) is reported as Unchecked, never as clean. It honors ctx between
// segments (MTIX-265).
func FindGaps(ctx context.Context, peersDir, selfPeerID string,
	cursor func(peerID string) (Position, error), keys KeySelector) (GapReport, error) {
	peers, _, err := segment.ListPeers(peersDir)
	if err != nil {
		return GapReport{}, fmt.Errorf("list relay peers: %w", err)
	}
	var rep GapReport
	for _, peer := range peers {
		if peer.PeerID == selfPeerID {
			continue
		}
		pos, curErr := cursor(peer.PeerID)
		if curErr != nil {
			return GapReport{}, fmt.Errorf("read ingest cursor for peer %s: %w", peer.PeerID, curErr)
		}
		gap, unchecked, walkErr := peerGap(ctx, peersDir, peer, pos, keys)
		if walkErr != nil {
			return GapReport{}, walkErr
		}
		if gap != nil {
			rep.Gaps = append(rep.Gaps, *gap)
		}
		if unchecked != nil {
			rep.Unchecked = append(rep.Unchecked, *unchecked)
		}
	}
	return rep, nil
}

// peerGap walks one peer's unread segments exactly as ingestPeer does. It
// returns a gap, or an Unchecked when the walk stopped early, or neither. A
// segments directory or segment that cannot be read is reported with
// RELAY_MEDIUM_UNREADABLE and its relay-relative path, as tick reports it
// (FR-21 section 9, MTIX-290).
func peerGap(ctx context.Context, peersDir string, peer segment.PeerDir, pos Position, keys KeySelector) (*Gap, *Unchecked, error) {
	dir := filepath.Join(peer.Path, segmentsSubdir)
	segs, _, err := segment.ListSegments(dir)
	if err != nil {
		return nil, &Unchecked{PeerID: peer.PeerID,
			Reason: unreadableReason(peersDir, dir, "segments directory unreadable", err)}, nil
	}
	if len(segs) == 0 {
		return nil, nil, nil
	}
	newest := segs[len(segs)-1].No
	ch := newChain(pos)
	unread := Unread(segs, pos)
	for i, f := range unread {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, fmt.Errorf("continuity walk of peer %s: %w", peer.PeerID, ctxErr)
		}
		raw, err := readSegment(f.Path)
		if err != nil {
			return nil, stoppedAt(peer.PeerID, f.No, unreadableReason(peersDir, f.Path, "unreadable", err)), nil
		}
		h, err := segment.ParseHeader(raw)
		if err != nil {
			return nil, stoppedAt(peer.PeerID, f.No, "damaged header: "+err.Error()), nil
		}
		if cErr := ch.check(peer.PeerID, h); cErr != nil {
			return &Gap{PeerID: peer.PeerID, Reason: cErr.Error()}, nil, nil
		}
		in := scanInput{raw: raw, header: h, prev: ch.pos,
			sealed: f.No < newest, peerID: peer.PeerID, keys: keys, peersDir: peersDir}
		if i+1 < len(unread) {
			in.next = &unread[i+1]
		}
		reached, scanErr := scanReached(in)
		if scanErr != nil {
			return nil, stoppedAt(peer.PeerID, f.No, scanErr.Error()), nil
		}
		if reached.torn {
			ch.advanceTorn(reached.pos, reached.resumeRS)
		} else {
			ch.advance(reached.pos)
		}
	}
	return nil, nil, nil
}

// stoppedAt builds the Unchecked row for a walk that stopped at a segment.
func stoppedAt(peerID string, no uint64, why string) *Unchecked {
	return &Unchecked{PeerID: peerID, Reason: fmt.Sprintf("checked up to before segment %d: %s", no, why)}
}

// scanInput is one segment a continuity walk reads without applying.
type scanInput struct {
	raw    []byte
	header segment.Header
	prev   Position
	sealed bool
	peerID string
	keys   KeySelector
	next   *segment.File
	// peersDir names a medium fault by its relay-relative path.
	peersDir string
}

// keyFor resolves the key a segment's records are authenticated with.
func (in scanInput) keyFor(h segment.Header) ([]byte, error) {
	if !h.Authenticated() {
		return nil, nil
	}
	if in.keys == nil {
		return nil, errors.New("authenticated segment but no relay keys are available")
	}
	k, err := in.keys.For(h.KeyEpoch)
	if err != nil {
		return nil, fmt.Errorf("key epoch %d unavailable: %w", h.KeyEpoch, err)
	}
	return k, nil
}

// nextPath is the successor segment's path, or "" when there is none.
func (in scanInput) nextPath() string {
	if in.next == nil {
		return ""
	}
	return in.next.Path
}

// reachedScan is where a read-only scan ended. torn marks a sealed segment
// whose torn tail the successor explains (FR-21 5.5), with resumeRS the
// relay sequence just before the successor's first record.
type reachedScan struct {
	pos      Position
	torn     bool
	resumeRS uint64
}

// scanReached reads a segment without applying it and returns where it ends,
// by the same rule ingestSegment uses. The error says why it cannot be read.
func scanReached(in scanInput) (reachedScan, error) {
	key, err := in.keyFor(in.header)
	if err != nil {
		return reachedScan{}, err
	}
	sc, err := segment.NewScanner(bytes.NewReader(in.raw), segment.ScanOptions{
		Sealed: in.sealed, Key: key, ExpectPeerID: in.peerID,
	})
	if err != nil {
		return reachedScan{}, fmt.Errorf("segment cannot be scanned: %w", err)
	}
	reached := Position{SegmentNo: in.header.SegmentNo, RS: in.prev.RS, PubEpoch: in.header.PubEpoch}
	if in.header.PubEpoch != in.prev.PubEpoch {
		reached.RS = in.header.FirstRS - 1
	}
	base := reached.RS
	for sc.Next() {
		if rs := sc.Record().RS; rs > base {
			reached.RS = rs
		}
	}
	scErr := sc.Err()
	if scErr != nil && in.sealed && segment.IsIncomplete(scErr) {
		ts := newTornSeal(in.peerID, in.raw, sc, in.next, in.keyFor)
		succ, ok, readErr := ts.explain()
		if mv := mediumVerdict(in.peersDir, in.nextPath(), readErr); readErr != nil && mv != nil {
			return reachedScan{}, mv // the successor could not be read: not a corrupt tail
		}
		if ok {
			return reachedScan{pos: reached, torn: true, resumeRS: succ.FirstRS - 1}, nil
		}
	}
	if scErr != nil {
		return reachedScan{}, fmt.Errorf("records cannot be read: %w", scErr)
	}
	return reachedScan{pos: reached}, nil
}

// StampedPosition adds the publisher epoch a snapshot stamped to a cursor that
// `relay clone` seeded (segment 0, epoch 0). The cursor itself stays as base
// wrote it, so the store advances it on the first poll; the stamp feeds the
// continuity check only (MTIX-265).
func StampedPosition(pos Position, stampEpoch uint16) Position {
	if pos.SegmentNo == 0 && pos.PubEpoch == 0 {
		pos.PubEpoch = stampEpoch
	}
	return pos
}

// chainStart is where the continuity walk of one peer begins this poll.
func (in *Ingestor) chainStart(ctx context.Context, peerID string, cur sqlite.RelayIngestPosition) (Position, error) {
	pos := Position{SegmentNo: cur.SegmentNo, RS: cur.RS, PubEpoch: cur.PubEpoch}
	if pos.SegmentNo != 0 || pos.PubEpoch != 0 {
		return pos, nil
	}
	epoch, err := in.cfg.Store.RelayCloneStampEpoch(ctx, peerID)
	if err != nil {
		return Position{}, fmt.Errorf("read relay clone stamp for peer %s: %w", peerID, err)
	}
	return StampedPosition(pos, epoch), nil
}
