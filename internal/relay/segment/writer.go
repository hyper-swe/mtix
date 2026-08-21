// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// segmentFileMode is the permission for a segment file. The relay
// medium is shared, so nothing here relaxes beyond owner access.
const segmentFileMode os.FileMode = 0o600

// WriterConfig configures a peer's publisher for its own segment
// directory. A peer writes only under its own directory (FR-21 §4), so
// no file in this design ever has two writers and there is no
// cross-host mutual exclusion to get wrong.
type WriterConfig struct {
	// Dir is this peer's own segment directory. It must already exist.
	Dir string

	// PeerID is this peer's identity, per the §5.2 grammar.
	PeerID string

	// Key is the fleet MAC key. Required unless Unauthenticated is set.
	Key []byte

	// Unauthenticated selects the §8.3 mode, in which every record
	// carries an all-zero tag. It is explicit rather than inferred
	// from a nil key so a key that failed to load can never silently
	// downgrade a fleet to unauthenticated publishing.
	Unauthenticated bool

	// KeyEpoch identifies the key in use. A change from what the
	// medium holds opens a fresh segment, so every record in a file
	// shares one key epoch and a reader mid-rotation can tell an old
	// valid key from a wrong one by the frame alone.
	KeyEpoch uint16

	// PubEpoch is the publisher restore epoch (§5.7). A change from
	// what the medium holds requires NextRS: the new epoch restarts
	// relay sequences at a base the operator declares.
	PubEpoch uint16

	// NextRS, when non-zero, declares the relay sequence the next
	// record must carry — the caller's durable cursor. The writer
	// verifies the medium agrees and refuses with ErrPeerIDConflict
	// when it does not (§5.2). It is required across a publisher epoch
	// change, where there is no continuation to infer.
	NextRS uint64

	// MaxSegmentBytes is the rotation threshold; zero means
	// DefaultMaxSegmentBytes.
	MaxSegmentBytes int64
}

// Writer appends framed records to a peer's own segment directory,
// rotating when a segment fills.
//
// Nothing here depends on a primitive ADR-005 rules out. There are no
// locks: the single-writer-per-directory invariant removes the need for
// them. There is no fsync ordering: each frame is written in one call
// and validated by content at read time, so correctness never rests on
// when — or whether — the medium flushed. Nothing is ever rewritten in
// place, which is what lets a reader treat sealed bytes as immutable.
//
// A Writer is not safe for concurrent use, by design: two goroutines
// appending would be two writers on one file, the exact shape the
// transport exists to avoid.
type Writer struct {
	dir       string
	header    Header
	key       []byte
	maxBytes  int64
	f         *os.File
	size      int64
	nextRS    uint64
	records   int64
	recovered bool
}

// NewWriter opens a peer's publisher, resuming its own stream from the
// medium.
//
// On startup it validates its own newest segment (FR-21 §5.5). An
// intact tail is continued. A torn tail — a crash mid-append, possibly
// the writer's own pre-crash bytes still buffered in a cache it can no
// longer trust — is sealed by rotating past it, never truncated or
// overwritten: in-place repair on a bridged medium races a remote
// cache's view, and the duplicate records that recovery may produce are
// absorbed downstream by event dedupe. A newest segment that is not
// this peer's own continuation is refused before a byte is written
// (§5.2).
func NewWriter(cfg WriterConfig) (*Writer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	w := &Writer{
		dir:      cfg.Dir,
		key:      cfg.Key,
		maxBytes: cfg.MaxSegmentBytes,
		header: Header{
			FormatVersion: FormatVersion,
			PeerID:        cfg.PeerID,
			KeyEpoch:      cfg.KeyEpoch,
			PubEpoch:      cfg.PubEpoch,
		},
	}
	if w.maxBytes == 0 {
		w.maxBytes = DefaultMaxSegmentBytes
	}
	if !cfg.Unauthenticated {
		w.header.Flags |= FlagAuthenticated
	}

	tail, err := w.inspectTail(cfg)
	if err != nil {
		return nil, err
	}
	if tail.open {
		if err := w.reopen(tail); err != nil {
			return nil, err
		}
		return w, nil
	}
	if err := w.openNew(tail.segmentNo, tail.nextRS); err != nil {
		return nil, err
	}
	return w, nil
}

// validate refuses a configuration that could not publish.
func (c WriterConfig) validate() error {
	if err := ValidatePeerID(c.PeerID); err != nil {
		return fmt.Errorf("relay writer: %w", err)
	}
	if c.Unauthenticated == (len(c.Key) != 0) {
		return fmt.Errorf("%w: exactly one of Key and Unauthenticated must be set", ErrUnauthenticatedKey)
	}
	if c.MaxSegmentBytes < 0 {
		return fmt.Errorf("relay writer: max segment bytes is negative")
	}
	return lstatDir(c.Dir)
}

// tailState is what the medium says about this peer's newest segment.
type tailState struct {
	open      bool   // the newest segment may be appended to
	path      string // set when open
	header    Header // set when open: the header already on the medium
	size      int64  // set when open
	records   int64  // set when open
	segmentNo uint64 // the segment to open or continue
	nextRS    uint64 // the relay sequence the next record carries
}

// inspectTail reads this peer's newest segment and decides whether to
// continue it, rotate past it, or refuse.
func (w *Writer) inspectTail(cfg WriterConfig) (tailState, error) {
	segs, _, err := ListSegments(w.dir)
	if err != nil {
		return tailState{}, err
	}
	if len(segs) == 0 {
		return w.freshStream(cfg)
	}

	newest := segs[len(segs)-1]
	res, err := ScanFile(newest.Path, ScanOptions{
		Key:          cfg.Key,
		ExpectPeerID: cfg.PeerID,
	})
	if err != nil {
		// A header this peer cannot read leaves no safe continuation
		// to infer, and guessing one would publish under relay
		// sequences a reader may already have consumed.
		return tailState{}, fmt.Errorf("relay writer: newest segment %s: %w", newest.Name, err)
	}

	resumeRS := res.Cursor.RS + 1
	if err := w.checkDeclaredCursor(cfg, res.Header, resumeRS); err != nil {
		return tailState{}, err
	}
	if cfg.PubEpoch != res.Header.PubEpoch {
		// The restore epoch changed: republish into fresh segments
		// from the base the operator declared.
		return tailState{segmentNo: newest.No + 1, nextRS: cfg.NextRS}, nil
	}
	if res.Truncated {
		w.recovered = true
		return tailState{segmentNo: newest.No + 1, nextRS: resumeRS}, nil
	}
	if cfg.KeyEpoch != res.Header.KeyEpoch {
		return tailState{segmentNo: newest.No + 1, nextRS: resumeRS}, nil
	}
	return tailState{
		open:      true,
		path:      newest.Path,
		header:    res.Header,
		size:      newest.Size,
		records:   int64(len(res.Records)),
		segmentNo: newest.No,
		nextRS:    resumeRS,
	}, nil
}

// freshStream decides where a peer with no segments starts.
func (w *Writer) freshStream(cfg WriterConfig) (tailState, error) {
	next := cfg.NextRS
	if next == 0 {
		next = 1
	}
	return tailState{segmentNo: 1, nextRS: next}, nil
}

// checkDeclaredCursor is the FR-21 §5.2 self-check. A configured peer
// id rests on the operator not running two live writers under it, so
// the writer confirms on every start that the medium agrees with its
// durable cursor — and refuses publication rather than reconciling a
// disagreement it cannot explain.
func (w *Writer) checkDeclaredCursor(cfg WriterConfig, h Header, resumeRS uint64) error {
	if cfg.PubEpoch != h.PubEpoch {
		if cfg.NextRS == 0 {
			return fmt.Errorf("relay writer: publisher epoch %d needs a declared base relay sequence",
				cfg.PubEpoch)
		}
		return nil
	}
	if cfg.NextRS != 0 && cfg.NextRS != resumeRS {
		return fmt.Errorf("%w: cursor expects rs %d, the medium continues at rs %d",
			ErrPeerIDConflict, cfg.NextRS, resumeRS)
	}
	return nil
}

// reopen continues an intact active segment.
func (w *Writer) reopen(tail tailState) error {
	f, err := os.OpenFile(tail.path, os.O_WRONLY|os.O_APPEND, segmentFileMode) // #nosec G304 -- path comes from the Lstat-verified relay walker
	if err != nil {
		return fmt.Errorf("open segment %s: %w", tail.path, err)
	}
	w.f = f
	w.size = tail.size
	w.records = tail.records
	// Adopt the header already on the medium rather than a
	// reconstruction of it: it is the MAC context every record in this
	// file was authenticated against, down to the format version a
	// different build may have written.
	w.header = tail.header
	w.nextRS = tail.nextRS
	return nil
}

// openNew creates a segment and writes its header.
//
// Exclusive creation is a sanity check, not a correctness mechanism:
// FR-21 does not trust the medium to make it atomic, and the design
// removes the need for cross-host mutual exclusion rather than
// attempting it. It still catches the local mistakes it can.
func (w *Writer) openNew(segmentNo, firstRS uint64) error {
	h := w.header
	h.SegmentNo = segmentNo
	h.FirstRS = firstRS
	raw, err := h.MarshalBinary()
	if err != nil {
		return err
	}
	path := filepath.Join(w.dir, FileName(segmentNo))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, segmentFileMode) // #nosec G304 -- name is rendered by FileName from a validated segment number
	if err != nil {
		return fmt.Errorf("create segment %s: %w", path, err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return fmt.Errorf("write segment header %s: %w", path, err)
	}
	w.f = f
	w.header = h
	w.size = int64(len(raw))
	w.records = 0
	w.nextRS = firstRS
	return nil
}

// SegmentNo returns the segment currently being appended to.
func (w *Writer) SegmentNo() uint64 { return w.header.SegmentNo }

// NextRS returns the relay sequence the next appended record carries.
func (w *Writer) NextRS() uint64 { return w.nextRS }

// Cursor returns the position this writer has published through.
func (w *Writer) Cursor() Cursor {
	return Cursor{SegmentNo: w.header.SegmentNo, RS: w.nextRS - 1, PubEpoch: w.header.PubEpoch}
}

// RecoveredFromTornTail reports whether startup sealed a damaged tail
// by rotating past it (FR-21 §5.5). Callers republish from their
// durable cursor when it is set; the duplicates that produces are
// absorbed downstream.
func (w *Writer) RecoveredFromTornTail() bool { return w.recovered }

// Append frames one payload and writes it to the active segment,
// returning the relay sequence it was published under.
//
// The whole frame goes out in a single write, so a torn append can only
// ever truncate one frame — never interleave two — which is the
// condition the reader's tail rule is written against.
func (w *Writer) Append(payload []byte) (uint64, error) {
	if w.f == nil {
		return 0, errors.New("relay writer: closed")
	}
	if len(payload) > MaxPayloadBytes {
		// Refused before anything is written and before a relay
		// sequence is consumed, so a rejected payload leaves no hole
		// in the stream for a reader to stall on.
		return 0, fmt.Errorf("%w: payload is %d bytes, cap is %d",
			ErrPayloadTooLarge, len(payload), MaxPayloadBytes)
	}
	frameSize := int64(RecordHeaderSize + len(payload))
	if w.records > 0 && w.size+frameSize > w.maxBytes {
		// Rotate only once the segment holds something. A record
		// larger than the threshold is carried whole in a segment of
		// its own rather than rotating forever or being split.
		if err := w.Rotate(); err != nil {
			return 0, err
		}
	}
	frame, err := AppendRecord(nil, w.header, w.nextRS, payload, w.key)
	if err != nil {
		return 0, err
	}
	n, err := w.f.Write(frame)
	w.size += int64(n)
	if err != nil {
		return 0, fmt.Errorf("append to segment %d: %w", w.header.SegmentNo, err)
	}
	rs := w.nextRS
	w.nextRS++
	w.records++
	return rs, nil
}

// Rotate seals the active segment and opens its successor. Rotation is
// the only commit primitive this design needs (FR-21 §5.3): a segment
// becomes immutable by virtue of a successor existing, with nothing
// rewritten and no rename to trust.
func (w *Writer) Rotate() error {
	if w.f == nil {
		return errors.New("relay writer: closed")
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("close segment %d: %w", w.header.SegmentNo, err)
	}
	w.f = nil
	return w.openNew(w.header.SegmentNo+1, w.nextRS)
}

// Close releases the active segment. It is idempotent, and it flushes
// nothing beyond the writes already issued: durability lives in the
// publisher's own journal, and the relay copy is a projection that any
// peer can re-derive.
func (w *Writer) Close() error {
	if w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	if err := f.Close(); err != nil {
		return fmt.Errorf("close segment %d: %w", w.header.SegmentNo, err)
	}
	return nil
}
