// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// The frame and header parsers are the most attacker-writable byte
// surface in the system: they read a directory other software can write
// to, across a boundary with no server in the middle. FR-21 §11.10
// makes fuzzing them an exit criterion for this deliverable.
//
// The contracts both targets enforce:
//
//   - Never panic, never hang, never allocate from an unvalidated
//     length. Arbitrary bytes are expected input, not an error case.
//   - Never report success on bytes that do not round-trip. A parser
//     that accepts something it cannot reproduce has accepted a record
//     the publisher never wrote.

// fuzzSeeds returns byte sequences worth starting from: real frames,
// near-misses, and the shapes that historically break binary parsers.
func fuzzSeeds(t *testing.F, framed []byte) {
	t.Helper()
	seeds := [][]byte{
		nil,
		{},
		[]byte("MREC"),
		[]byte("MRSG"),
		bytes.Repeat([]byte{0}, segment.RecordHeaderSize),
		bytes.Repeat([]byte{0xff}, segment.RecordHeaderSize),
		bytes.Repeat([]byte{0}, segment.HeaderSize),
		framed,
		framed[:len(framed)-1],
		framed[:segment.RecordHeaderSize],
	}
	// A frame claiming the maximum possible payload with no payload
	// behind it — the classic length-prefix attack.
	oversize := bytes.Clone(framed[:segment.RecordHeaderSize])
	binary.BigEndian.PutUint32(oversize[4:8], 0xffffffff)
	seeds = append(seeds, oversize)

	atCap := bytes.Clone(framed[:segment.RecordHeaderSize])
	binary.BigEndian.PutUint32(atCap[4:8], uint32(segment.MaxPayloadBytes))
	seeds = append(seeds, atCap)

	for _, s := range seeds {
		t.Add(s)
	}
}

// FuzzParseRecord drives arbitrary bytes through the record parser in
// the context of a valid header, and asserts that anything it accepts
// re-encodes to exactly the bytes it consumed.
func FuzzParseRecord(f *testing.F) {
	h := testHeader()
	framed, err := segment.AppendRecord(nil, h, h.FirstRS, []byte(`{"event_id":"seed"}`), testKey)
	require.NoError(f, err)
	fuzzSeeds(f, framed)

	f.Fuzz(func(t *testing.T, data []byte) {
		rec, n, err := segment.ParseRecord(data, h, testKey)
		if err != nil {
			// Every rejection is either "not here yet" or a
			// corruption-class verdict. A third category would mean a
			// caller cannot decide what to do from the error alone.
			require.True(t, segment.IsIncomplete(err) || segment.CodeOf(err) == "RELAY_SEGMENT_CORRUPT",
				"unclassified verdict: %v", err)
			require.Zero(t, n, "a rejected frame consumes nothing")
			return
		}

		require.GreaterOrEqual(t, n, segment.RecordHeaderSize)
		require.LessOrEqual(t, n, len(data))
		require.Len(t, rec.Payload, n-segment.RecordHeaderSize)
		require.Equal(t, segment.CRC32C(rec.Payload), rec.CRC32C)

		// Re-framing what was accepted must reproduce it byte for byte.
		// Anything else would mean the parser accepted a record no
		// publisher could have written.
		reframed, err := segment.AppendRecord(nil, h, rec.RS, rec.Payload, testKey)
		require.NoError(t, err)
		require.Equal(t, data[:n], reframed)

		// Trailing bytes must not change how the frame itself parses.
		again, n2, err := segment.ParseRecord(data[:n], h, testKey)
		require.NoError(t, err)
		require.Equal(t, n, n2)
		require.Equal(t, rec.RS, again.RS)
	})
}

// FuzzParseHeader drives arbitrary bytes through the segment header
// parser and asserts the same round-trip property.
func FuzzParseHeader(f *testing.F) {
	raw, err := testHeader().MarshalBinary()
	require.NoError(f, err)
	fuzzSeeds(f, raw)
	f.Add(append(bytes.Clone(raw), []byte("trailing record bytes")...))

	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := segment.ParseHeader(data)
		if err != nil {
			require.True(t, segment.IsIncomplete(err) ||
				segment.CodeOf(err) == "RELAY_SEGMENT_CORRUPT" ||
				segment.CodeOf(err) == "RELAY_FOREIGN_ENTRY",
				"unclassified verdict: %v", err)
			return
		}

		require.NoError(t, segment.ValidatePeerID(h.PeerID))
		require.NotZero(t, h.SegmentNo)
		require.NotZero(t, h.FirstRS)

		// An accepted header must re-encode to the bytes it was read
		// from. Any difference means the parser tolerated a header it
		// would not itself write — the gap where a crafted segment
		// gets to mean two things at once.
		reencoded, err := h.MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, data[:segment.HeaderSize], reencoded)

		// Whatever follows the header must not have changed it.
		again, err := segment.ParseHeader(data[:segment.HeaderSize])
		require.NoError(t, err)
		require.Equal(t, h, again)
	})
}

// FuzzScanSegment drives arbitrary bytes through the whole segment
// reader in both lifecycle states. The scanner is where the tail rule
// and the contiguity rule meet, so it gets its own surface rather than
// being covered only through its parsers.
func FuzzScanSegment(f *testing.F) {
	h := testHeader()
	whole := buildSegment(f, h, testKey, "one", "two", "three")
	fuzzSeeds(f, whole)
	for cut := 0; cut < len(whole); cut += 7 {
		f.Add(whole[:cut])
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, sealed := range []bool{false, true} {
			res, err := segment.ScanAll(bytes.NewReader(data), segment.ScanOptions{
				Sealed: sealed, Key: testKey,
			})
			if err != nil {
				require.True(t, segment.IsIncomplete(err) || segment.CodeOf(err) != "",
					"unclassified verdict: %v", err)
				continue
			}
			// Whatever came back must be internally consistent: the
			// records form an unbroken run from the header's declared
			// start, which is the invariant every layer above relies on
			// to detect a gap.
			for i, rec := range res.Records {
				require.Equal(t, res.Header.FirstRS+uint64(i), rec.RS)
				require.Equal(t, segment.CRC32C(rec.Payload), rec.CRC32C)
			}
			if len(res.Records) > 0 {
				require.Equal(t, res.Records[len(res.Records)-1].RS, res.Cursor.RS)
			}
		}
	})
}
