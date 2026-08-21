// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// Wire constants of the FR-21 §5.3 segment format. Changing any of them
// is a format-major bump (§10), never a quiet edit.
const (
	// MagicSegment opens a segment file.
	MagicSegment = "MRSG"

	// MagicRecord opens a record frame.
	MagicRecord = "MREC"

	// HeaderSize is the fixed segment header width: 4 magic + 2
	// format_version + 2 flags + 64 peer_id + 8 segment_no + 8 first_rs
	// + 2 key_epoch + 2 pub_epoch.
	HeaderSize = 92

	// RecordHeaderSize is the fixed part of a record frame: 4 magic +
	// 4 payload_len + 8 rs + 4 crc32c + 32 mac. The payload follows.
	RecordHeaderSize = 52

	// MACSize is the HMAC-SHA256 width.
	MACSize = 32

	// peerIDFieldSize is the NUL-padded width of peer_id in the header.
	peerIDFieldSize = 64

	// MaxPayloadBytes caps a record payload at the FR-21 §5.3 limit:
	// the 64 KiB event cap plus envelope headroom. A length prefix
	// above it is corruption and is never honored — in particular the
	// parser never sizes an allocation from an unvalidated length.
	MaxPayloadBytes = 128 * 1024

	// DefaultMaxSegmentBytes is the default rotation threshold
	// (FR-21 §5.3, sync.relay.max_segment_bytes).
	DefaultMaxSegmentBytes = 4 << 20
)

// FormatVersion is the format version this build writes: u16 packed as
// major<<8|minor (FR-21 §10). Major must match to read; a higher minor
// is additive and readable.
const FormatVersion uint16 = 0x0100

// FlagAuthenticated is header flags bit 0 (FR-21 §5.3). When clear the
// relay runs in the unauthenticated mode of §8.3 and every mac field is
// all zeroes. Mixing modes within one relay is a refusal, so a reader
// never has to decide which record to trust.
const FlagAuthenticated uint16 = 1 << 0

// byteOrder is big-endian throughout the frame, including the MAC
// pre-image. FR-21 §5.3 fixes the field order but not the integer
// encoding; network byte order is chosen here so the format reads the
// same on every peer regardless of host architecture, and is pinned by
// the layout tests.
var byteOrder = binary.BigEndian

// castagnoli is the CRC32C table (FR-21 §5.3). Built once; the value is
// immutable and shared, which keeps this package allocation-free on the
// hot path without introducing any mutable global state.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// formatMajor extracts the major half of a packed format version.
func formatMajor(v uint16) uint8 { return uint8(v >> 8) }

// Header is the FR-21 §5.3 segment header. It supplies the context every
// record in the file is authenticated against, which is why a record can
// never be read — or relocated — without it.
type Header struct {
	// FormatVersion is major<<8|minor (FR-21 §10).
	FormatVersion uint16

	// Flags carries FlagAuthenticated in bit 0. Unknown bits are
	// ignored by readers, per the additive-minor rule of §10.
	Flags uint16

	// PeerID is the writing peer, per the §5.2 grammar.
	PeerID string

	// SegmentNo is this segment's strictly-consecutive number,
	// starting at 1. Naming carries ordering; no manifest is
	// load-bearing.
	SegmentNo uint64

	// FirstRS is the relay sequence of this segment's first record.
	FirstRS uint64

	// KeyEpoch identifies the key this segment's records are MAC'd
	// with, so a reader mid-rotation can tell "old key, valid,
	// pre-boundary" from "wrong key, forged" from the frame itself
	// rather than from mutable metadata.
	KeyEpoch uint16

	// PubEpoch is the publisher restore epoch (FR-21 §5.7). Readers key
	// contiguity on (PubEpoch, rs), never on rs alone, so a restored
	// publisher's new work can never land silently on positions a
	// reader already consumed.
	PubEpoch uint16
}

// Authenticated reports whether this segment carries MACs.
func (h Header) Authenticated() bool {
	return h.Flags&FlagAuthenticated != 0
}

// Validate checks the invariants a header must satisfy to be written or
// to be trusted after parsing.
func (h Header) Validate() error {
	if formatMajor(h.FormatVersion) != formatMajor(FormatVersion) {
		return fmt.Errorf("%w: major %d, this build reads %d",
			ErrBadFormatVersion, formatMajor(h.FormatVersion), formatMajor(FormatVersion))
	}
	if err := ValidatePeerID(h.PeerID); err != nil {
		return fmt.Errorf("%w: %v", ErrBadPeerIDField, err)
	}
	if h.SegmentNo == 0 {
		return fmt.Errorf("%w: segment_no is zero", ErrSegmentCorrupt)
	}
	if h.FirstRS == 0 {
		// rs is a strictly-contiguous counter starting at 1, so zero
		// is "absent", never a position.
		return fmt.Errorf("%w: first_rs is zero", ErrSegmentCorrupt)
	}
	return nil
}

// MarshalBinary encodes the header. A header that would not survive
// ParseHeader is refused here rather than written to the medium.
func (h Header) MarshalBinary() ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	b := make([]byte, HeaderSize)
	copy(b[0:4], MagicSegment)
	byteOrder.PutUint16(b[4:6], h.FormatVersion)
	byteOrder.PutUint16(b[6:8], h.Flags)
	copy(b[8:8+peerIDFieldSize], h.PeerID)
	byteOrder.PutUint64(b[72:80], h.SegmentNo)
	byteOrder.PutUint64(b[80:88], h.FirstRS)
	byteOrder.PutUint16(b[88:90], h.KeyEpoch)
	byteOrder.PutUint16(b[90:92], h.PubEpoch)
	return b, nil
}

// ParseHeader decodes a segment header from the front of b.
//
// A short input reports ErrIncomplete rather than damage: a segment file
// observed mid-creation on a shared medium is a normal sight, and the
// caller decides what that means from the segment's lifecycle state.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("%w: header needs %d bytes, have %d",
			ErrIncomplete, HeaderSize, len(b))
	}
	if string(b[0:4]) != MagicSegment {
		return Header{}, fmt.Errorf("%w: want %q at the head of the segment", ErrBadMagic, MagicSegment)
	}
	peerID, err := decodePeerIDField(b[8 : 8+peerIDFieldSize])
	if err != nil {
		return Header{}, err
	}
	h := Header{
		FormatVersion: byteOrder.Uint16(b[4:6]),
		Flags:         byteOrder.Uint16(b[6:8]),
		PeerID:        peerID,
		SegmentNo:     byteOrder.Uint64(b[72:80]),
		FirstRS:       byteOrder.Uint64(b[80:88]),
		KeyEpoch:      byteOrder.Uint16(b[88:90]),
		PubEpoch:      byteOrder.Uint16(b[90:92]),
	}
	if err := h.Validate(); err != nil {
		return Header{}, err
	}
	return h, nil
}

// decodePeerIDField reads the NUL-padded peer_id field. Padding must be
// all NUL: trailing junk after the terminator is a medium other software
// wrote to, or a crafted header, and either way is not this format.
func decodePeerIDField(field []byte) (string, error) {
	end := bytes.IndexByte(field, 0)
	if end < 0 {
		end = len(field)
	}
	for _, c := range field[end:] {
		if c != 0 {
			return "", fmt.Errorf("%w: padding after the peer id is not NUL", ErrBadPeerIDField)
		}
	}
	id := string(field[:end])
	if err := ValidatePeerID(id); err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadPeerIDField, err)
	}
	return id, nil
}

// Record is one parsed record frame.
type Record struct {
	// RS is this record's relay sequence within its peer's stream.
	RS uint64

	// CRC32C is the Castagnoli checksum of Payload, as carried on the
	// wire and already verified by ParseRecord.
	CRC32C uint32

	// MAC is the record's authentication tag, all zeroes in
	// unauthenticated mode.
	MAC [MACSize]byte

	// Payload is the canonical JSON of one sync event. It aliases the
	// buffer it was parsed from; callers that retain it past the
	// buffer's life must copy.
	Payload []byte
}

// CRC32C returns the Castagnoli checksum FR-21 §5.3 puts in every frame.
func CRC32C(payload []byte) uint32 {
	return crc32.Checksum(payload, castagnoli)
}

// ComputeMAC returns the FR-21 §5.3 record MAC:
//
//	HMAC-SHA256(key[key_epoch],
//	    format_version ‖ peer_id ‖ segment_no ‖ key_epoch ‖ pub_epoch ‖
//	    rs ‖ crc32c ‖ payload)
//
// Binding the position and both epochs into the tag is what makes a
// record non-relocatable: a genuine record replayed into another
// position, file, peer, or epoch fails authentication. In
// unauthenticated mode the tag is all zeroes.
func ComputeMAC(h Header, rs uint64, crc uint32, payload, key []byte) [MACSize]byte {
	var out [MACSize]byte
	if !h.Authenticated() {
		return out
	}
	m := hmac.New(sha256.New, key)
	var fixed [2 + peerIDFieldSize + 8 + 2 + 2 + 8 + 4]byte
	byteOrder.PutUint16(fixed[0:2], h.FormatVersion)
	copy(fixed[2:2+peerIDFieldSize], h.PeerID)
	byteOrder.PutUint64(fixed[66:74], h.SegmentNo)
	byteOrder.PutUint16(fixed[74:76], h.KeyEpoch)
	byteOrder.PutUint16(fixed[76:78], h.PubEpoch)
	byteOrder.PutUint64(fixed[78:86], rs)
	byteOrder.PutUint32(fixed[86:90], crc)
	m.Write(fixed[:])
	m.Write(payload)
	m.Sum(out[:0])
	return out
}

// checkAuthMode enforces the FR-21 §5.3 rule that a relay is either
// authenticated or not, with no per-call mixing: a key handed to an
// unauthenticated segment, or missing from an authenticated one, is a
// configuration error refused rather than resolved.
func checkAuthMode(h Header, key []byte) error {
	switch {
	case h.Authenticated() && len(key) == 0:
		return fmt.Errorf("%w: segment is authenticated but no key was supplied", ErrUnauthenticatedKey)
	case !h.Authenticated() && len(key) != 0:
		return fmt.Errorf("%w: segment is unauthenticated but a key was supplied", ErrUnauthenticatedKey)
	default:
		return nil
	}
}

// AppendRecord frames one payload for h at rs and appends it to dst,
// returning the extended slice. Callers append a whole frame in a single
// write so a torn append can only ever truncate a frame, never interleave
// two.
func AppendRecord(dst []byte, h Header, rs uint64, payload, key []byte) ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	if err := checkAuthMode(h, key); err != nil {
		return nil, err
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: payload is %d bytes, cap is %d",
			ErrPayloadTooLarge, len(payload), MaxPayloadBytes)
	}
	crc := CRC32C(payload)
	mac := ComputeMAC(h, rs, crc, payload, key)

	var frame [RecordHeaderSize]byte
	copy(frame[0:4], MagicRecord)
	// #nosec G115 -- len(payload) is capped at MaxPayloadBytes above.
	byteOrder.PutUint32(frame[4:8], uint32(len(payload)))
	byteOrder.PutUint64(frame[8:16], rs)
	byteOrder.PutUint32(frame[16:20], crc)
	copy(frame[20:52], mac[:])

	dst = append(dst, frame[:]...)
	dst = append(dst, payload...)
	return dst, nil
}

// framePayloadLen validates a record frame's fixed header and returns
// its payload length. It is the one place a length prefix from the
// medium is admitted, and it admits nothing above the cap: FR-21 §5.3
// treats a larger prefix as corruption, so no caller ever sizes a read
// or an allocation from an unvalidated number.
func framePayloadLen(fixed []byte) (int, error) {
	if string(fixed[0:4]) != MagicRecord {
		return 0, fmt.Errorf("%w: want %q at the head of the record", ErrBadMagic, MagicRecord)
	}
	payloadLen := byteOrder.Uint32(fixed[4:8])
	if payloadLen > MaxPayloadBytes {
		return 0, fmt.Errorf("%w: length prefix %d, cap is %d",
			ErrPayloadTooLarge, payloadLen, MaxPayloadBytes)
	}
	return int(payloadLen), nil
}

// ParseRecord decodes one record frame from the front of b in the
// context of h, returning the record and the number of bytes consumed.
//
// The verdicts, in the order they are reached:
//
//	ErrIncomplete       the frame is not all here yet
//	ErrBadMagic         these bytes do not open a record
//	ErrPayloadTooLarge  the length prefix exceeds the cap (never honored)
//	ErrCRCMismatch      the payload does not match its checksum
//	ErrMACMismatch      the record is not authentic in this position
//
// Every verdict except ErrIncomplete is corruption-class. Whether that
// is fatal depends on where the frame was found: at the tail of the
// active segment it means "not yet written" and the reader stops
// cleanly; in a sealed segment it is damage and the reader stalls loudly
// (FR-21 §5.4). That decision belongs to the reader, not here.
func ParseRecord(b []byte, h Header, key []byte) (Record, int, error) {
	if err := checkAuthMode(h, key); err != nil {
		return Record{}, 0, err
	}
	if len(b) < RecordHeaderSize {
		return Record{}, 0, fmt.Errorf("%w: record header needs %d bytes, have %d",
			ErrIncomplete, RecordHeaderSize, len(b))
	}
	payloadLen, err := framePayloadLen(b[:RecordHeaderSize])
	if err != nil {
		return Record{}, 0, err
	}
	total := RecordHeaderSize + payloadLen
	if len(b) < total {
		return Record{}, 0, fmt.Errorf("%w: record needs %d bytes, have %d",
			ErrIncomplete, total, len(b))
	}

	rec := Record{
		RS:      byteOrder.Uint64(b[8:16]),
		CRC32C:  byteOrder.Uint32(b[16:20]),
		Payload: b[RecordHeaderSize:total],
	}
	copy(rec.MAC[:], b[20:52])

	if got := CRC32C(rec.Payload); got != rec.CRC32C {
		return Record{}, 0, fmt.Errorf("%w: computed %#08x, frame carries %#08x",
			ErrCRCMismatch, got, rec.CRC32C)
	}
	want := ComputeMAC(h, rec.RS, rec.CRC32C, rec.Payload, key)
	if !hmac.Equal(want[:], rec.MAC[:]) {
		return Record{}, 0, fmt.Errorf("%w: at rs %d in segment %d", ErrMACMismatch, rec.RS, h.SegmentNo)
	}
	return rec, total, nil
}
