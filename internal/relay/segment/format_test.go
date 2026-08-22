// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// testKey is the fixed key this layer verifies against. Key lifecycle
// (sourcing, rotation, epoch boundary records) arrives with FR-21.2;
// the frame is designed for it now so the MAC layout never changes
// under a shipped reader.
var testKey = []byte("fr21-segment-layer-fixed-test-key")

func testHeader() segment.Header {
	return segment.Header{
		FormatVersion: segment.FormatVersion,
		Flags:         segment.FlagAuthenticated,
		PeerID:        testPeerID,
		SegmentNo:     7,
		FirstRS:       100,
		KeyEpoch:      2,
		PubEpoch:      3,
	}
}

// wantMAC recomputes the FR-21 §5.3 MAC independently of the
// implementation, straight from the spec's concatenation order:
// format_version ‖ flags ‖ peer_id ‖ segment_no ‖ key_epoch ‖ pub_epoch
// ‖ rs ‖ crc32c ‖ payload, every integer big-endian and peer_id in its
// 64-byte NUL-padded field form.
//
// first_rs is absent by design: §5.3 leaves it unbound because the
// first record's MAC-bound rs must equal it, which catches a forged
// value structurally.
func wantMAC(t *testing.T, h segment.Header, rs uint64, crc uint32, payload, key []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.BigEndian, h.FormatVersion))
	require.NoError(t, binary.Write(&buf, binary.BigEndian, h.Flags))
	peer := make([]byte, 64)
	copy(peer, h.PeerID)
	buf.Write(peer)
	require.NoError(t, binary.Write(&buf, binary.BigEndian, h.SegmentNo))
	require.NoError(t, binary.Write(&buf, binary.BigEndian, h.KeyEpoch))
	require.NoError(t, binary.Write(&buf, binary.BigEndian, h.PubEpoch))
	require.NoError(t, binary.Write(&buf, binary.BigEndian, rs))
	require.NoError(t, binary.Write(&buf, binary.BigEndian, crc))
	buf.Write(payload)

	m := hmac.New(sha256.New, key)
	m.Write(buf.Bytes())
	return m.Sum(nil)
}

// TestFrameSizes pins the wire sizes. A change here is a format-major
// bump (FR-21 §10), never a quiet edit.
func TestFrameSizes(t *testing.T) {
	require.Equal(t, 92, segment.HeaderSize, "4 magic + 2 version + 2 flags + 64 peer + 8 seg + 8 rs + 2 + 2")
	require.Equal(t, 52, segment.RecordHeaderSize, "4 magic + 4 len + 8 rs + 4 crc + 32 mac")
	require.Equal(t, 32, segment.MACSize)
	require.Equal(t, 128*1024, segment.MaxPayloadBytes)
	require.Equal(t, uint16(0x0100), segment.FormatVersion, "major 1, minor 0")
}

// TestHeader_MarshalBinary_Layout asserts the FR-21 §5.3 header field
// by field at its byte offset, independently of how the encoder is
// written. This is the specification, expressed as a test.
func TestHeader_MarshalBinary_Layout(t *testing.T) {
	h := testHeader()
	b, err := h.MarshalBinary()
	require.NoError(t, err)
	require.Len(t, b, segment.HeaderSize)

	require.Equal(t, "MRSG", string(b[0:4]), "magic")
	require.Equal(t, segment.FormatVersion, binary.BigEndian.Uint16(b[4:6]), "format_version")
	require.Equal(t, segment.FlagAuthenticated, binary.BigEndian.Uint16(b[6:8]), "flags")
	require.Equal(t, testPeerID, string(bytes.TrimRight(b[8:72], "\x00")), "peer_id")
	require.Equal(t, bytes.Repeat([]byte{0}, 64-len(testPeerID)), b[8+len(testPeerID):72], "peer_id NUL padding")
	require.Equal(t, uint64(7), binary.BigEndian.Uint64(b[72:80]), "segment_no")
	require.Equal(t, uint64(100), binary.BigEndian.Uint64(b[80:88]), "first_rs")
	require.Equal(t, uint16(2), binary.BigEndian.Uint16(b[88:90]), "key_epoch")
	require.Equal(t, uint16(3), binary.BigEndian.Uint16(b[90:92]), "pub_epoch")
}

// TestHeader_RoundTrip covers both authentication modes and the id
// label form.
func TestHeader_RoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		header segment.Header
	}{
		{"authenticated", testHeader()},
		{"unauthenticated", segment.Header{
			FormatVersion: segment.FormatVersion,
			PeerID:        testPeerID,
			SegmentNo:     1,
			FirstRS:       1,
		}},
		{"labelled peer id", segment.Header{
			FormatVersion: segment.FormatVersion,
			Flags:         segment.FlagAuthenticated,
			PeerID:        testPeerID + "-courier-laptop",
			SegmentNo:     4294967296,
			FirstRS:       18446744073709551615,
			KeyEpoch:      65535,
			PubEpoch:      65535,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := tt.header.MarshalBinary()
			require.NoError(t, err)
			got, err := segment.ParseHeader(b)
			require.NoError(t, err)
			require.Equal(t, tt.header, got)
			require.Equal(t, tt.header.Flags&segment.FlagAuthenticated != 0, got.Authenticated())
		})
	}
}

// TestHeader_MarshalBinary_RejectsUnwritableHeaders stops a malformed
// header at the writer rather than letting it reach the medium.
func TestHeader_MarshalBinary_RejectsUnwritableHeaders(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*segment.Header)
	}{
		{"empty peer id", func(h *segment.Header) { h.PeerID = "" }},
		{"peer id off grammar", func(h *segment.Header) { h.PeerID = "NOT-A-PEER" }},
		{"segment number zero", func(h *segment.Header) { h.SegmentNo = 0 }},
		{"first rs zero", func(h *segment.Header) { h.FirstRS = 0 }},
		{"wrong format major", func(h *segment.Header) { h.FormatVersion = 0x0200 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := testHeader()
			tt.mutate(&h)
			_, err := h.MarshalBinary()
			require.Error(t, err)
		})
	}
}

// TestParseHeader_Rejections covers every way a header fails to be a
// header. Short input is "not written yet"; everything else is damage.
func TestParseHeader_Rejections(t *testing.T) {
	good, err := testHeader().MarshalBinary()
	require.NoError(t, err)

	tests := []struct {
		name    string
		input   func() []byte
		wantErr error
	}{
		{"empty", func() []byte { return nil }, segment.ErrIncomplete},
		{"one byte short", func() []byte { return good[:segment.HeaderSize-1] }, segment.ErrIncomplete},
		{"magic only", func() []byte { return good[:4] }, segment.ErrIncomplete},
		{"bad magic", func() []byte {
			b := bytes.Clone(good)
			copy(b[0:4], "XXXX")
			return b
		}, segment.ErrBadMagic},
		{"record magic in header slot", func() []byte {
			b := bytes.Clone(good)
			copy(b[0:4], "MREC")
			return b
		}, segment.ErrBadMagic},
		{"unsupported format major", func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint16(b[4:6], 0x0200)
			return b
		}, segment.ErrBadFormatVersion},
		{"peer id not NUL padded", func() []byte {
			b := bytes.Clone(good)
			b[71] = 'x'
			return b
		}, segment.ErrBadPeerIDField},
		{"peer id off grammar", func() []byte {
			b := bytes.Clone(good)
			b[0+8] = 'Z'
			return b
		}, segment.ErrBadPeerIDField},
		{"peer id empty", func() []byte {
			b := bytes.Clone(good)
			copy(b[8:72], make([]byte, 64))
			return b
		}, segment.ErrBadPeerIDField},
		{"segment number zero", func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint64(b[72:80], 0)
			return b
		}, segment.ErrSegmentCorrupt},
		{"first rs zero", func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint64(b[80:88], 0)
			return b
		}, segment.ErrSegmentCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := segment.ParseHeader(tt.input())
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestParseHeader_AcceptsAdditiveMinorVersion is the FR-21 §10 rule:
// minor bumps are additive, so a reader must not refuse a segment from
// a slightly newer publisher.
func TestParseHeader_AcceptsAdditiveMinorVersion(t *testing.T) {
	b, err := testHeader().MarshalBinary()
	require.NoError(t, err)
	binary.BigEndian.PutUint16(b[4:6], 0x0109)
	h, err := segment.ParseHeader(b)
	require.NoError(t, err)
	require.Equal(t, uint16(0x0109), h.FormatVersion)
}

// TestParseHeader_IgnoresUnknownFlagBits is the other half of §10:
// a reader ignores flags it does not know rather than stalling on them.
func TestParseHeader_IgnoresUnknownFlagBits(t *testing.T) {
	b, err := testHeader().MarshalBinary()
	require.NoError(t, err)
	binary.BigEndian.PutUint16(b[6:8], segment.FlagAuthenticated|0x8000)
	h, err := segment.ParseHeader(b)
	require.NoError(t, err)
	require.True(t, h.Authenticated())
}

// TestAppendRecord_Layout asserts the FR-21 §5.3 record frame field by
// field at its byte offset, and checks the CRC and MAC against
// independently computed values.
func TestAppendRecord_Layout(t *testing.T) {
	h := testHeader()
	payload := []byte(`{"event_id":"01a0238b","op_type":"create_node"}`)

	b, err := segment.AppendRecord(nil, h, 100, payload, testKey)
	require.NoError(t, err)
	require.Len(t, b, segment.RecordHeaderSize+len(payload))

	require.Equal(t, "MREC", string(b[0:4]), "magic")
	require.Equal(t, uint32(len(payload)), binary.BigEndian.Uint32(b[4:8]), "payload_len")
	require.Equal(t, uint64(100), binary.BigEndian.Uint64(b[8:16]), "rs")

	crc := crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli))
	require.Equal(t, crc, binary.BigEndian.Uint32(b[16:20]), "crc32c Castagnoli")
	require.Equal(t, crc, segment.CRC32C(payload))

	require.Equal(t, wantMAC(t, h, 100, crc, payload, testKey), b[20:52], "mac")
	require.Equal(t, payload, b[52:], "payload")
}

// TestRecord_RoundTrip covers payload shapes including the empty and
// maximum-size cases.
func TestRecord_RoundTrip(t *testing.T) {
	h := testHeader()
	tests := []struct {
		name    string
		payload []byte
	}{
		{"typical event", []byte(`{"event_id":"x"}`)},
		{"empty payload", []byte{}},
		{"single byte", []byte{0}},
		{"binary payload", bytes.Repeat([]byte{0xff, 0x00}, 64)},
		{"at the cap", bytes.Repeat([]byte("a"), segment.MaxPayloadBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := segment.AppendRecord(nil, h, 100, tt.payload, testKey)
			require.NoError(t, err)
			rec, n, err := segment.ParseRecord(b, h, testKey)
			require.NoError(t, err)
			require.Equal(t, len(b), n)
			require.Equal(t, uint64(100), rec.RS)
			require.Equal(t, tt.payload, rec.Payload)
			require.Equal(t, segment.CRC32C(tt.payload), rec.CRC32C)
		})
	}
}

// TestAppendRecord_AppendsToExistingBuffer keeps the writer's
// one-write-per-frame shape available to callers.
func TestAppendRecord_AppendsToExistingBuffer(t *testing.T) {
	h := testHeader()
	prefix := []byte("PRE")
	b, err := segment.AppendRecord(prefix, h, 100, []byte("x"), testKey)
	require.NoError(t, err)
	require.Equal(t, prefix, b[:3])
	_, n, err := segment.ParseRecord(b[3:], h, testKey)
	require.NoError(t, err)
	require.Equal(t, len(b)-3, n)
}

// TestAppendRecord_RejectsOversizePayload stops a too-large payload at
// the writer; FR-21 §5.3 caps a record's payload at 128 KiB.
func TestAppendRecord_RejectsOversizePayload(t *testing.T) {
	h := testHeader()
	_, err := segment.AppendRecord(nil, h, 100, bytes.Repeat([]byte("a"), segment.MaxPayloadBytes+1), testKey)
	require.ErrorIs(t, err, segment.ErrPayloadTooLarge)
}

// TestParseRecord_ShortInputIsIncompleteAtEveryBoundary walks the frame
// one byte at a time. Every prefix short of the whole frame must read
// as "not yet written" — this is the per-frame half of the FR-21 §5.4
// tail rule, and the reason a reader never has to guess.
func TestParseRecord_ShortInputIsIncompleteAtEveryBoundary(t *testing.T) {
	h := testHeader()
	payload := []byte("hello relay")
	full, err := segment.AppendRecord(nil, h, 100, payload, testKey)
	require.NoError(t, err)

	for i := 0; i < len(full); i++ {
		_, _, err := segment.ParseRecord(full[:i], h, testKey)
		require.True(t, segment.IsIncomplete(err),
			"prefix of %d/%d bytes must read as incomplete, got %v", i, len(full), err)
	}
	_, n, err := segment.ParseRecord(full, h, testKey)
	require.NoError(t, err)
	require.Equal(t, len(full), n)
}

// TestParseRecord_Rejections covers every damage class in one table.
func TestParseRecord_Rejections(t *testing.T) {
	h := testHeader()
	payload := []byte("hello relay")
	good, err := segment.AppendRecord(nil, h, 100, payload, testKey)
	require.NoError(t, err)

	tests := []struct {
		name    string
		input   func() []byte
		wantErr error
	}{
		{"bad magic", func() []byte {
			b := bytes.Clone(good)
			copy(b[0:4], "XXXX")
			return b
		}, segment.ErrBadMagic},
		{"segment magic in record slot", func() []byte {
			b := bytes.Clone(good)
			copy(b[0:4], "MRSG")
			return b
		}, segment.ErrBadMagic},
		{"length above the cap", func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint32(b[4:8], uint32(segment.MaxPayloadBytes)+1)
			return b
		}, segment.ErrPayloadTooLarge},
		{"length at uint32 max", func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint32(b[4:8], 0xffffffff)
			return b
		}, segment.ErrPayloadTooLarge},
		{"payload bit flipped", func() []byte {
			b := bytes.Clone(good)
			b[len(b)-1] ^= 0x01
			return b
		}, segment.ErrCRCMismatch},
		{"crc field tampered", func() []byte {
			b := bytes.Clone(good)
			b[16] ^= 0xff
			return b
		}, segment.ErrCRCMismatch},
		{"mac bit flipped", func() []byte {
			b := bytes.Clone(good)
			b[20] ^= 0x01
			return b
		}, segment.ErrMACMismatch},
		{"mac zeroed on an authenticated segment", func() []byte {
			b := bytes.Clone(good)
			copy(b[20:52], make([]byte, 32))
			return b
		}, segment.ErrMACMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := segment.ParseRecord(tt.input(), h, testKey)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorIs(t, err, segment.ErrSegmentCorrupt)
		})
	}
}

// TestParseRecord_OversizeLengthIsNeverHonored is the allocation guard
// of FR-21 §5.3: a length prefix above the cap is corruption, so the
// parser must refuse it without sizing anything from it.
func TestParseRecord_OversizeLengthIsNeverHonored(t *testing.T) {
	h := testHeader()
	b := make([]byte, segment.RecordHeaderSize)
	copy(b[0:4], "MREC")
	binary.BigEndian.PutUint32(b[4:8], 0xffffffff)
	_, n, err := segment.ParseRecord(b, h, testKey)
	require.ErrorIs(t, err, segment.ErrPayloadTooLarge)
	require.Zero(t, n)
}

// TestRecordMAC_BindsPositionAndEpochs is the FR-21 §5.3 non-relocation
// property: a genuine, correctly-MAC'd record replayed at a different
// position, in a different file, from a different peer, or under a
// different epoch fails authentication. That single property closes the
// reorder, replay, and rollback splices — an attacker who can write the
// medium still cannot move a record the fleet already trusts.
func TestRecordMAC_BindsPositionAndEpochs(t *testing.T) {
	h := testHeader()
	payload := []byte("bound to its position")
	framed, err := segment.AppendRecord(nil, h, 100, payload, testKey)
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*segment.Header, *[]byte)
	}{
		{"replayed at a different rs", func(_ *segment.Header, b *[]byte) {
			binary.BigEndian.PutUint64((*b)[8:16], 101)
		}},
		{"spliced into a different segment", func(h *segment.Header, _ *[]byte) {
			h.SegmentNo = 8
		}},
		{"attributed to a different peer", func(h *segment.Header, _ *[]byte) {
			h.PeerID = "fedcba9876543210"
		}},
		{"rolled back to an older key epoch", func(h *segment.Header, _ *[]byte) {
			h.KeyEpoch = 1
		}},
		{"rolled back to an older publisher epoch", func(h *segment.Header, _ *[]byte) {
			h.PubEpoch = 2
		}},
		{"read under a different format version", func(h *segment.Header, _ *[]byte) {
			h.FormatVersion = 0x0101
		}},
		{"read under a different flag word", func(h *segment.Header, _ *[]byte) {
			// Still authenticated, so this exercises the flags binding
			// itself rather than the mode-mismatch refusal.
			h.Flags = segment.FlagAuthenticated | 0x8000
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := h
			b := bytes.Clone(framed)
			tt.mutate(&ctx, &b)
			_, _, err := segment.ParseRecord(b, ctx, testKey)
			require.ErrorIs(t, err, segment.ErrMACMismatch)
		})
	}
}

// TestRecordMAC_WrongKeyFails covers the outside-the-fleet attacker and
// the wrong-key attach.
func TestRecordMAC_WrongKeyFails(t *testing.T) {
	h := testHeader()
	b, err := segment.AppendRecord(nil, h, 100, []byte("x"), testKey)
	require.NoError(t, err)
	_, _, err = segment.ParseRecord(b, h, []byte("a different fleet key"))
	require.ErrorIs(t, err, segment.ErrMACMismatch)
}

// TestUnauthenticatedMode is FR-21 §5.3/§8.3: with the flag clear the
// mac field is all zeroes, and mixing the two modes is refused rather
// than resolved.
func TestUnauthenticatedMode(t *testing.T) {
	h := segment.Header{
		FormatVersion: segment.FormatVersion,
		PeerID:        testPeerID,
		SegmentNo:     1,
		FirstRS:       1,
	}
	require.False(t, h.Authenticated())

	b, err := segment.AppendRecord(nil, h, 1, []byte("plain"), nil)
	require.NoError(t, err)
	require.Equal(t, make([]byte, 32), b[20:52], "mac field must be all zeroes")

	rec, n, err := segment.ParseRecord(b, h, nil)
	require.NoError(t, err)
	require.Equal(t, len(b), n)
	require.Equal(t, []byte("plain"), rec.Payload)

	t.Run("corruption is still caught by CRC", func(t *testing.T) {
		bad := bytes.Clone(b)
		bad[len(bad)-1] ^= 0x01
		_, _, err := segment.ParseRecord(bad, h, nil)
		require.ErrorIs(t, err, segment.ErrCRCMismatch)
	})

	t.Run("non-zero mac with the flag clear", func(t *testing.T) {
		bad := bytes.Clone(b)
		bad[20] = 0x01
		_, _, err := segment.ParseRecord(bad, h, nil)
		require.ErrorIs(t, err, segment.ErrMACMismatch)
	})

	t.Run("key supplied for an unauthenticated segment", func(t *testing.T) {
		_, _, err := segment.ParseRecord(b, h, testKey)
		require.ErrorIs(t, err, segment.ErrUnauthenticatedKey)
		_, err = segment.AppendRecord(nil, h, 1, []byte("x"), testKey)
		require.ErrorIs(t, err, segment.ErrUnauthenticatedKey)
	})

	t.Run("key missing for an authenticated segment", func(t *testing.T) {
		auth := testHeader()
		good, err := segment.AppendRecord(nil, auth, 100, []byte("x"), testKey)
		require.NoError(t, err)
		_, _, err = segment.ParseRecord(good, auth, nil)
		require.ErrorIs(t, err, segment.ErrUnauthenticatedKey)
		_, err = segment.AppendRecord(nil, auth, 100, []byte("x"), nil)
		require.ErrorIs(t, err, segment.ErrUnauthenticatedKey)
	})
}

// TestParseHeader_PeerIDFieldWithoutTerminator covers a field packed
// edge to edge with no NUL. The §5.2 grammar tops out at 49 characters,
// so 64 unterminated bytes are never a peer id — but the decoder must
// reach that verdict without reading past the field.
func TestParseHeader_PeerIDFieldWithoutTerminator(t *testing.T) {
	b, err := testHeader().MarshalBinary()
	require.NoError(t, err)
	copy(b[8:72], bytes.Repeat([]byte("a"), 64))
	_, err = segment.ParseHeader(b)
	require.ErrorIs(t, err, segment.ErrBadPeerIDField)
}

// TestAppendRecord_RefusesAnInvalidHeader stops a malformed context at
// the writer: a record framed under a header that cannot be parsed back
// would be unreadable by every peer.
func TestAppendRecord_RefusesAnInvalidHeader(t *testing.T) {
	h := testHeader()
	h.PeerID = "not-a-peer-id"
	_, err := segment.AppendRecord(nil, h, 100, []byte("x"), testKey)
	require.ErrorIs(t, err, segment.ErrBadPeerIDField)
}
