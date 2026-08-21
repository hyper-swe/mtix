// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Cursor is a reader's position in one peer's stream. It is the durable
// ingest position of FR-21 §6.3, and it carries the publisher epoch
// because contiguity is keyed on (PubEpoch, RS) and never on RS alone.
type Cursor struct {
	// SegmentNo is the segment the position lies in. Zero means
	// nothing has been read from this peer yet.
	SegmentNo uint64

	// RS is the highest relay sequence consumed.
	RS uint64

	// PubEpoch is the publisher epoch RS is measured in.
	PubEpoch uint16
}

// ScanOptions configures a scan of one segment.
type ScanOptions struct {
	// Sealed marks the segment immutable because a successor exists.
	// It selects between the two halves of the FR-21 §5.4 tail rule,
	// and it is the single most consequential field in this package:
	// setting it wrongly either stalls a healthy fleet or accepts a
	// truncated sealed segment as complete.
	Sealed bool

	// Key is the fleet MAC key, nil in unauthenticated mode.
	Key []byte

	// ExpectPeerID, when set, requires the header to name this peer.
	// A segment filed under one peer that claims another has either
	// been relocated or was written by something that is not this
	// peer's publisher.
	ExpectPeerID string
}

// Result is the outcome of scanning one segment.
type Result struct {
	// Header is the segment's validated header.
	Header Header

	// Records are the records that validated, in stream order, with
	// payloads copied out of the scan buffer.
	Records []Record

	// Truncated reports that an active segment stopped at an
	// unfinished tail — the expected steady state of a publisher
	// mid-append, and a reason to poll again rather than to worry.
	Truncated bool

	// Cursor is the position after the last delivered record. With no
	// records delivered it is the position before the segment's first.
	Cursor Cursor
}

// Scanner walks the records of one segment, applying the FR-21 §5.4
// tail rule and the §5.3 contiguity rule as it goes.
//
// A Scanner never skips. Once it stops — cleanly at an unfinished tail,
// or loudly on damage or a gap — it delivers nothing further from that
// segment. Skipping past a bad frame could silently drop a causal
// predecessor, and for a coordination fabric a loud stall is strictly
// better than a quiet hole.
type Scanner struct {
	r         io.Reader
	header    Header
	key       []byte
	sealed    bool
	buf       []byte
	rec       Record
	next      uint64 // relay sequence the next record must carry
	delivered uint64 // relay sequence of the last delivered record
	truncated bool
	done      bool
	err       error
}

// NewScanner reads and validates the segment header, then prepares to
// walk the records after it.
//
// A short header on an active segment reports ErrIncomplete: a segment
// file observed between creation and its header write is a normal sight
// on a shared medium. The same input on a sealed segment is damage. A
// malformed header is damage in either state — a header is written once
// at creation and never appended to, so no append in flight can explain
// it.
func NewScanner(r io.Reader, opts ScanOptions) (*Scanner, error) {
	raw := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, raw); err != nil {
		if !isShortRead(err) {
			return nil, fmt.Errorf("read segment header: %w", err)
		}
		short := fmt.Errorf("%w: segment header is not fully written", ErrIncomplete)
		if opts.Sealed {
			return nil, fmt.Errorf("%w: sealed segment header is short", ErrSegmentCorrupt)
		}
		return nil, short
	}
	h, err := ParseHeader(raw)
	if err != nil {
		return nil, err
	}
	if opts.ExpectPeerID != "" && h.PeerID != opts.ExpectPeerID {
		return nil, fmt.Errorf("%w: segment names peer %q, expected %q",
			ErrPeerIDConflict, h.PeerID, opts.ExpectPeerID)
	}
	if err := checkAuthMode(h, opts.Key); err != nil {
		return nil, err
	}
	return &Scanner{
		r:         r,
		header:    h,
		key:       opts.Key,
		sealed:    opts.Sealed,
		buf:       make([]byte, RecordHeaderSize),
		next:      h.FirstRS,
		delivered: h.FirstRS - 1,
	}, nil
}

// Header returns the validated segment header.
func (s *Scanner) Header() Header { return s.header }

// Record returns the record the last Next call delivered.
func (s *Scanner) Record() Record { return s.rec }

// Err returns the verdict that stopped the scan, or nil if it stopped
// at the end of the segment or at an unfinished active tail.
func (s *Scanner) Err() error { return s.err }

// Truncated reports whether an active segment stopped short of its end.
func (s *Scanner) Truncated() bool { return s.truncated }

// Cursor returns the position after the last delivered record.
func (s *Scanner) Cursor() Cursor {
	return Cursor{SegmentNo: s.header.SegmentNo, RS: s.delivered, PubEpoch: s.header.PubEpoch}
}

// Next advances to the next record, returning false at the end of the
// segment or at the first verdict that stops the scan.
func (s *Scanner) Next() bool {
	if s.done {
		return false
	}
	n, err := io.ReadFull(s.r, s.buf[:RecordHeaderSize])
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			s.done = true // the segment ends on a frame boundary
			return false
		}
		return s.stopShort(err)
	}
	payloadLen, err := framePayloadLen(s.buf[:RecordHeaderSize])
	if err != nil {
		return s.stopDamaged(err)
	}
	total := RecordHeaderSize + payloadLen
	if cap(s.buf) < total {
		grown := make([]byte, total)
		copy(grown, s.buf[:RecordHeaderSize])
		s.buf = grown
	}
	s.buf = s.buf[:total]
	if _, readErr := io.ReadFull(s.r, s.buf[RecordHeaderSize:total]); readErr != nil {
		return s.stopShort(readErr)
	}
	rec, _, err := ParseRecord(s.buf[:total], s.header, s.key)
	if err != nil {
		return s.stopDamaged(err)
	}
	if rec.RS != s.next {
		// Whole, authentic bytes in the wrong place. This is not a torn
		// write, so the active-tail exemption does not apply: stopping
		// quietly would let the reader resume past a dropped
		// predecessor on its next poll.
		return s.stop(fmt.Errorf("%w: segment %d expected rs %d, found %d",
			ErrGap, s.header.SegmentNo, s.next, rec.RS))
	}
	s.rec = rec
	s.delivered = rec.RS
	s.next = rec.RS + 1
	return true
}

// stopShort handles a read that ended before the frame did.
func (s *Scanner) stopShort(err error) bool {
	if !isShortRead(err) {
		// A medium error is neither damage nor an append in flight;
		// it is reported as itself so the caller retries the medium
		// rather than condemning the segment.
		return s.stop(fmt.Errorf("read segment %d: %w", s.header.SegmentNo, err))
	}
	return s.stopDamaged(fmt.Errorf("%w: frame ends early", ErrIncomplete))
}

// stopDamaged applies the FR-21 §5.4 tail rule to a frame that is not
// valid data: at the tail of the active segment it is an append the
// reader has not seen finish, and in a sealed segment it is damage.
func (s *Scanner) stopDamaged(err error) bool {
	if !s.sealed {
		s.truncated = true
		s.done = true
		return false
	}
	if IsIncomplete(err) {
		err = fmt.Errorf("%w: sealed segment ends mid-frame after rs %d", ErrSegmentCorrupt, s.delivered)
	}
	return s.stop(err)
}

// stop records a verdict and ends the scan.
func (s *Scanner) stop(err error) bool {
	s.err = err
	s.done = true
	return false
}

// isShortRead reports whether err is the reader running out of bytes
// rather than the medium failing.
func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// ScanAll walks a whole segment and collects its records, copying each
// payload out of the scan buffer so the result outlives the reader.
func ScanAll(r io.Reader, opts ScanOptions) (Result, error) {
	sc, err := NewScanner(r, opts)
	if err != nil {
		return Result{}, err
	}
	res := Result{Header: sc.Header()}
	for sc.Next() {
		rec := sc.Record()
		rec.Payload = append([]byte(nil), rec.Payload...)
		res.Records = append(res.Records, rec)
	}
	if err := sc.Err(); err != nil {
		return Result{}, err
	}
	res.Truncated = sc.Truncated()
	res.Cursor = sc.Cursor()
	return res, nil
}

// ScanFile scans a segment from the medium under the same Lstat
// discipline as the walker: a symlink in a segment's place is refused
// rather than followed (FR-21 §5.1), and an entry that is not a regular
// file is refused before anything opens it.
func ScanFile(path string, opts ScanOptions) (Result, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Result{}, fmt.Errorf("lstat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Result{}, fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	if !fi.Mode().IsRegular() {
		return Result{}, fmt.Errorf("%w: %s is not a regular file", ErrForeignEntry, path)
	}
	f, err := os.Open(path) // #nosec G304 -- path comes from the Lstat-verified relay walker
	if err != nil {
		return Result{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ScanAll(f, opts)
}

// CheckContinuity reports whether the segment described by h may be
// read next, given the reader's position.
//
// Contiguity is keyed on (PubEpoch, RS) per FR-21 §5.3. Within one
// publisher epoch, segment numbers advance by one and relay sequences
// continue across the boundary; a skipped segment, a repeated position,
// or a sequence that jumps is a gap, and the reader stalls rather than
// stepping over it. A publisher epoch bump is the operator-gated
// restore of §5.7: it restarts relay sequences at a declared base, so
// the sequence check is suspended for that transition while segment
// numbers must still move forward. An epoch moving backwards is a
// rollback splice and is refused.
//
// A reader with no position expects the beginning of the stream. A peer
// joining a relay whose early segments were already pruned therefore
// gets a gap, which is the documented entry to the bootstrap path
// rather than a failure to handle here.
func CheckContinuity(prev Cursor, h Header) error {
	if prev.SegmentNo == 0 {
		if h.SegmentNo != 1 || h.FirstRS != 1 {
			return fmt.Errorf("%w: a reader with no position needs segment 1 from rs 1, found segment %d from rs %d",
				ErrGap, h.SegmentNo, h.FirstRS)
		}
		return nil
	}
	switch {
	case h.PubEpoch < prev.PubEpoch:
		return fmt.Errorf("%w: publisher epoch %d is behind the reader's epoch %d",
			ErrGap, h.PubEpoch, prev.PubEpoch)
	case h.PubEpoch > prev.PubEpoch:
		if h.SegmentNo <= prev.SegmentNo {
			return fmt.Errorf("%w: publisher epoch %d must arrive in a later segment than %d, found %d",
				ErrGap, h.PubEpoch, prev.SegmentNo, h.SegmentNo)
		}
		return nil
	}
	switch h.SegmentNo {
	case prev.SegmentNo:
		if h.FirstRS > prev.RS+1 {
			return fmt.Errorf("%w: segment %d restarts at rs %d, past the reader's position %d",
				ErrGap, h.SegmentNo, h.FirstRS, prev.RS)
		}
		return nil
	case prev.SegmentNo + 1:
		if h.FirstRS != prev.RS+1 {
			return fmt.Errorf("%w: segment %d starts at rs %d, expected %d",
				ErrGap, h.SegmentNo, h.FirstRS, prev.RS+1)
		}
		return nil
	default:
		return fmt.Errorf("%w: segment %d does not follow segment %d",
			ErrGap, h.SegmentNo, prev.SegmentNo)
	}
}
