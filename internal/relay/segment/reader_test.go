// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// buildSegment frames a whole segment: header plus one record per
// payload, at consecutive relay sequences from h.FirstRS.
func buildSegment(t testing.TB, h segment.Header, key []byte, payloads ...string) []byte {
	t.Helper()
	b, err := h.MarshalBinary()
	require.NoError(t, err)
	for i, p := range payloads {
		b, err = segment.AppendRecord(b, h, h.FirstRS+uint64(i), []byte(p), key)
		require.NoError(t, err)
	}
	return b
}

func sealedOpts() segment.ScanOptions {
	return segment.ScanOptions{Sealed: true, Key: testKey}
}

func activeOpts() segment.ScanOptions {
	return segment.ScanOptions{Key: testKey}
}

// payloadsOf collects the delivered payloads as strings.
func payloadsOf(recs []segment.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, string(r.Payload))
	}
	return out
}

// TestScan_ReadsEveryRecordInOrder is the happy path in both lifecycle
// states.
func TestScan_ReadsEveryRecordInOrder(t *testing.T) {
	h := testHeader()
	data := buildSegment(t, h, testKey, "one", "two", "three")

	for _, tt := range []struct {
		name string
		opts segment.ScanOptions
	}{
		{"active", activeOpts()},
		{"sealed", sealedOpts()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := segment.ScanAll(bytes.NewReader(data), tt.opts)
			require.NoError(t, err)
			require.Equal(t, h, res.Header)
			require.Equal(t, []string{"one", "two", "three"}, payloadsOf(res.Records))
			require.Equal(t, []uint64{100, 101, 102},
				[]uint64{res.Records[0].RS, res.Records[1].RS, res.Records[2].RS})
			require.False(t, res.Truncated)
			require.Equal(t, segment.Cursor{SegmentNo: 7, RS: 102, PubEpoch: 3}, res.Cursor)
		})
	}
}

// TestScan_EmptySegment covers a freshly rotated segment that has a
// header and nothing else — the normal state between rotation and the
// next append.
func TestScan_EmptySegment(t *testing.T) {
	h := testHeader()
	data := buildSegment(t, h, testKey)

	res, err := segment.ScanAll(bytes.NewReader(data), activeOpts())
	require.NoError(t, err)
	require.Empty(t, res.Records)
	require.False(t, res.Truncated)
	// Nothing applied from this segment yet, so the cursor still points
	// at the position before its first record.
	require.Equal(t, uint64(99), res.Cursor.RS)
}

// TestScan_ActiveTailStopsCleanly is the core of FR-21 §5.4. Every
// truncation of the last frame — one byte in, one byte short, and every
// point between — must yield the records before it and a clean stop,
// because on a shared medium that is simply an append the reader has
// not seen finish yet.
func TestScan_ActiveTailStopsCleanly(t *testing.T) {
	h := testHeader()
	full := buildSegment(t, h, testKey, "one", "two", "three")
	twoRecords := buildSegment(t, h, testKey, "one", "two")

	// The exact frame boundary is a clean prefix, not a torn tail: the
	// segment simply holds two records so far.
	res, err := segment.ScanAll(bytes.NewReader(twoRecords), activeOpts())
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, payloadsOf(res.Records))
	require.False(t, res.Truncated)

	for cut := len(twoRecords) + 1; cut < len(full); cut++ {
		res, err := segment.ScanAll(bytes.NewReader(full[:cut]), activeOpts())
		require.NoError(t, err, "cut at %d must not error on an active segment", cut)
		require.Equal(t, []string{"one", "two"}, payloadsOf(res.Records), "cut at %d", cut)
		require.True(t, res.Truncated, "cut at %d must report a truncated tail", cut)
	}
}

// TestScan_SealedSegmentTruncationIsCorruption is the same byte
// sequence read under the other lifecycle state. Sealed bytes are
// immutable, so a short tail there is damage or tampering: the reader
// stops loudly rather than skipping, because a skipped gap can silently
// drop a causal predecessor.
func TestScan_SealedSegmentTruncationIsCorruption(t *testing.T) {
	h := testHeader()
	full := buildSegment(t, h, testKey, "one", "two", "three")
	twoRecords := buildSegment(t, h, testKey, "one", "two")

	// A sealed segment that ends on a frame boundary is not damaged —
	// it simply holds fewer records. A missing record is caught one
	// level up, where the successor segment's first_rs has to continue
	// this one (see TestCheckContinuity).
	res, err := segment.ScanAll(bytes.NewReader(twoRecords), sealedOpts())
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, payloadsOf(res.Records))

	for cut := len(twoRecords) + 1; cut < len(full); cut++ {
		_, err := segment.ScanAll(bytes.NewReader(full[:cut]), sealedOpts())
		require.ErrorIs(t, err, segment.ErrSegmentCorrupt, "cut at %d", cut)
		require.Equal(t, "RELAY_SEGMENT_CORRUPT", segment.CodeOf(err))
	}
}

// TestScan_MidStreamDamageNeverSkips is the anti-skip rule stated
// directly: damage in the middle of a segment stops the scan at that
// point in both lifecycle states. In neither case may a reader step
// over it to reach the records beyond.
func TestScan_MidStreamDamageNeverSkips(t *testing.T) {
	h := testHeader()
	full := buildSegment(t, h, testKey, "one", "two", "three")
	oneRecord := buildSegment(t, h, testKey, "one")

	tests := []struct {
		name   string
		mutate func(b []byte)
	}{
		{"payload bit flipped", func(b []byte) { b[len(oneRecord)+segment.RecordHeaderSize] ^= 0x01 }},
		{"mac bit flipped", func(b []byte) { b[len(oneRecord)+20] ^= 0x01 }},
		{"magic clobbered", func(b []byte) { copy(b[len(oneRecord):len(oneRecord)+4], "XXXX") }},
		{"length prefix inflated", func(b []byte) {
			binary.BigEndian.PutUint32(b[len(oneRecord)+4:len(oneRecord)+8], uint32(segment.MaxPayloadBytes)+1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name+" in a sealed segment", func(t *testing.T) {
			b := bytes.Clone(full)
			tt.mutate(b)
			_, err := segment.ScanAll(bytes.NewReader(b), sealedOpts())
			require.ErrorIs(t, err, segment.ErrSegmentCorrupt)
		})
		t.Run(tt.name+" in an active segment stops at the damage", func(t *testing.T) {
			b := bytes.Clone(full)
			tt.mutate(b)
			res, err := segment.ScanAll(bytes.NewReader(b), activeOpts())
			require.NoError(t, err)
			require.Equal(t, []string{"one"}, payloadsOf(res.Records),
				"records past the damage must never be delivered")
			require.True(t, res.Truncated)
		})
	}
}

// TestScan_HeaderFailures covers the segment header in both states.
func TestScan_HeaderFailures(t *testing.T) {
	h := testHeader()
	full := buildSegment(t, h, testKey, "one")

	t.Run("empty active segment file is not yet written", func(t *testing.T) {
		_, err := segment.ScanAll(bytes.NewReader(nil), activeOpts())
		require.True(t, segment.IsIncomplete(err))
	})
	t.Run("short header on an active segment is not yet written", func(t *testing.T) {
		_, err := segment.ScanAll(bytes.NewReader(full[:segment.HeaderSize-1]), activeOpts())
		require.True(t, segment.IsIncomplete(err))
	})
	t.Run("short header on a sealed segment is corruption", func(t *testing.T) {
		_, err := segment.ScanAll(bytes.NewReader(full[:segment.HeaderSize-1]), sealedOpts())
		require.ErrorIs(t, err, segment.ErrSegmentCorrupt)
	})
	t.Run("a damaged header is corruption even on an active segment", func(t *testing.T) {
		// A header is written once, at creation, and never appended to.
		// Damage there cannot be an append in flight, so the §5.4
		// exemption does not apply.
		b := bytes.Clone(full)
		copy(b[0:4], "XXXX")
		_, err := segment.ScanAll(bytes.NewReader(b), activeOpts())
		require.ErrorIs(t, err, segment.ErrBadMagic)
		require.ErrorIs(t, err, segment.ErrSegmentCorrupt)
	})
}

// TestScan_RecordSequenceMustMatchTheHeader stops a file that lies
// about itself: the first record has to be the one the header declares,
// and each following record has to be its successor.
func TestScan_RecordSequenceMustMatchTheHeader(t *testing.T) {
	h := testHeader()

	t.Run("first record below first_rs", func(t *testing.T) {
		b, err := h.MarshalBinary()
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, h.FirstRS-1, []byte("early"), testKey)
		require.NoError(t, err)
		_, err = segment.ScanAll(bytes.NewReader(b), sealedOpts())
		require.ErrorIs(t, err, segment.ErrGap)
		require.Equal(t, "RELAY_GAP", segment.CodeOf(err))
	})

	t.Run("a skipped relay sequence", func(t *testing.T) {
		b, err := h.MarshalBinary()
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, 100, []byte("one"), testKey)
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, 102, []byte("three"), testKey)
		require.NoError(t, err)
		_, err = segment.ScanAll(bytes.NewReader(b), sealedOpts())
		require.ErrorIs(t, err, segment.ErrGap)
	})

	t.Run("a repeated relay sequence", func(t *testing.T) {
		b, err := h.MarshalBinary()
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, 100, []byte("one"), testKey)
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, 100, []byte("one again"), testKey)
		require.NoError(t, err)
		_, err = segment.ScanAll(bytes.NewReader(b), sealedOpts())
		require.ErrorIs(t, err, segment.ErrGap)
	})

	t.Run("a gap is loud even in an active segment", func(t *testing.T) {
		// A gap is not a torn write: the bytes are whole and
		// authentic, they are simply in the wrong place. Stopping
		// quietly would let the reader resume past a dropped
		// predecessor on the next poll.
		b, err := h.MarshalBinary()
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, 100, []byte("one"), testKey)
		require.NoError(t, err)
		b, err = segment.AppendRecord(b, h, 102, []byte("three"), testKey)
		require.NoError(t, err)
		_, err = segment.ScanAll(bytes.NewReader(b), activeOpts())
		require.ErrorIs(t, err, segment.ErrGap)
	})
}

// TestScan_PeerIDMismatch refuses a segment filed under the wrong peer.
func TestScan_PeerIDMismatch(t *testing.T) {
	h := testHeader()
	data := buildSegment(t, h, testKey, "one")

	opts := sealedOpts()
	opts.ExpectPeerID = "fedcba9876543210"
	_, err := segment.ScanAll(bytes.NewReader(data), opts)
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	require.Equal(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))

	opts.ExpectPeerID = testPeerID
	_, err = segment.ScanAll(bytes.NewReader(data), opts)
	require.NoError(t, err)
}

// TestScan_UnauthenticatedSegment reads a §8.3 relay end to end.
func TestScan_UnauthenticatedSegment(t *testing.T) {
	h := segment.Header{
		FormatVersion: segment.FormatVersion,
		PeerID:        testPeerID,
		SegmentNo:     1,
		FirstRS:       1,
	}
	data := buildSegment(t, h, nil, "one", "two")

	res, err := segment.ScanAll(bytes.NewReader(data), segment.ScanOptions{Sealed: true})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, payloadsOf(res.Records))

	t.Run("a key offered to an unauthenticated segment is refused", func(t *testing.T) {
		_, err := segment.ScanAll(bytes.NewReader(data), sealedOpts())
		require.ErrorIs(t, err, segment.ErrUnauthenticatedKey)
	})
	t.Run("an authenticated segment read without a key is refused", func(t *testing.T) {
		auth := buildSegment(t, testHeader(), testKey, "one")
		_, err := segment.ScanAll(bytes.NewReader(auth), segment.ScanOptions{Sealed: true})
		require.ErrorIs(t, err, segment.ErrUnauthenticatedKey)
	})
}

// TestScan_RecordsAreCopied guards against handing callers a view of a
// buffer the scanner reuses.
func TestScan_RecordsAreCopied(t *testing.T) {
	h := testHeader()
	data := buildSegment(t, h, testKey, "one", "two")
	res, err := segment.ScanAll(bytes.NewReader(data), sealedOpts())
	require.NoError(t, err)
	for i := range data {
		data[i] = 0
	}
	require.Equal(t, []string{"one", "two"}, payloadsOf(res.Records))
}

// TestScan_MaximumSizedRecords exercises the buffer growth path.
func TestScan_MaximumSizedRecords(t *testing.T) {
	h := testHeader()
	big := string(bytes.Repeat([]byte("z"), segment.MaxPayloadBytes))
	data := buildSegment(t, h, testKey, "small", big, "small again")
	res, err := segment.ScanAll(bytes.NewReader(data), sealedOpts())
	require.NoError(t, err)
	require.Len(t, res.Records, 3)
	require.Len(t, res.Records[1].Payload, segment.MaxPayloadBytes)
}

// TestCheckContinuity is the FR-21 §5.3 rule that readers key
// contiguity on (pub_epoch, rs) rather than on rs alone, applied across
// segment boundaries.
func TestCheckContinuity(t *testing.T) {
	h := func(segNo, firstRS uint64, pubEpoch uint16) segment.Header {
		return segment.Header{
			FormatVersion: segment.FormatVersion,
			Flags:         segment.FlagAuthenticated,
			PeerID:        testPeerID,
			SegmentNo:     segNo,
			FirstRS:       firstRS,
			PubEpoch:      pubEpoch,
		}
	}
	tests := []struct {
		name    string
		prev    segment.Cursor
		next    segment.Header
		wantErr bool
	}{
		{"a fresh reader starts at segment one", segment.Cursor{}, h(1, 1, 0), false},
		{"a fresh reader refuses a later segment", segment.Cursor{}, h(2, 51, 0), true},
		{"a fresh reader refuses a mid-stream first rs", segment.Cursor{}, h(1, 51, 0), true},
		{"the next segment continues the sequence",
			segment.Cursor{SegmentNo: 1, RS: 50}, h(2, 51, 0), false},
		{"the same segment is re-read from the start",
			segment.Cursor{SegmentNo: 2, RS: 60}, h(2, 51, 0), false},
		{"the same segment cannot start after the cursor",
			segment.Cursor{SegmentNo: 2, RS: 60}, h(2, 62, 0), true},
		{"a skipped segment is a gap",
			segment.Cursor{SegmentNo: 1, RS: 50}, h(3, 51, 0), true},
		{"a segment going backwards is a gap",
			segment.Cursor{SegmentNo: 3, RS: 50}, h(2, 51, 0), true},
		{"a break in relay sequence across the boundary is a gap",
			segment.Cursor{SegmentNo: 1, RS: 50}, h(2, 52, 0), true},
		{"a repeat across the boundary is a gap",
			segment.Cursor{SegmentNo: 1, RS: 50}, h(2, 50, 0), true},
		{"a publisher epoch bump restarts the sequence at a declared base",
			segment.Cursor{SegmentNo: 4, RS: 900, PubEpoch: 1}, h(5, 500, 2), false},
		{"a publisher epoch bump still moves forward through segments",
			segment.Cursor{SegmentNo: 4, RS: 900, PubEpoch: 1}, h(4, 500, 2), true},
		{"a publisher epoch rollback is refused",
			segment.Cursor{SegmentNo: 4, RS: 900, PubEpoch: 2}, h(5, 901, 1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := segment.CheckContinuity(tt.prev, tt.next)
			if tt.wantErr {
				require.ErrorIs(t, err, segment.ErrGap)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestScanFile reads through the same Lstat discipline the walker uses.
func TestScanFile(t *testing.T) {
	dir := t.TempDir()
	h := testHeader()
	path := filepath.Join(dir, segment.FileName(h.SegmentNo))
	require.NoError(t, os.WriteFile(path, buildSegment(t, h, testKey, "one", "two"), 0o600))

	res, err := segment.ScanFile(path, sealedOpts())
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, payloadsOf(res.Records))

	t.Run("a symlinked segment is refused rather than followed", func(t *testing.T) {
		link := filepath.Join(dir, segment.FileName(99))
		require.NoError(t, os.Symlink(path, link))
		_, err := segment.ScanFile(link, sealedOpts())
		require.ErrorIs(t, err, segment.ErrSymlink)
	})

	t.Run("a missing segment reports the medium error", func(t *testing.T) {
		_, err := segment.ScanFile(filepath.Join(dir, segment.FileName(42)), sealedOpts())
		require.Error(t, err)
		require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
	})
}

// failingReader delivers a prefix and then fails, standing in for the
// medium going away mid-read — an unreachable bridge mount, an ejected
// disk, an I/O error on a filer.
type failingReader struct {
	data []byte
	n    int
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n >= len(f.data) {
		return 0, f.err
	}
	n := copy(p, f.data[f.n:])
	f.n += n
	return n, nil
}

// TestScan_MediumErrorsAreNotCorruptionVerdicts keeps a failing medium
// distinct from damaged bytes. Condemning a segment because a mount
// dropped would turn a retryable outage into an operator ticket, and
// FR-21 §4 is explicit that relay files are a projection: losing access
// to them degrades latency, never integrity.
func TestScan_MediumErrorsAreNotCorruptionVerdicts(t *testing.T) {
	h := testHeader()
	full := buildSegment(t, h, testKey, "one", "two")
	ioErr := errors.New("input/output error")

	t.Run("while reading the header", func(t *testing.T) {
		_, err := segment.ScanAll(&failingReader{data: full[:10], err: ioErr}, activeOpts())
		require.ErrorIs(t, err, ioErr)
		require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
		require.False(t, segment.IsIncomplete(err))
	})

	t.Run("while reading a record header", func(t *testing.T) {
		_, err := segment.ScanAll(&failingReader{data: full[:segment.HeaderSize], err: ioErr}, activeOpts())
		require.ErrorIs(t, err, ioErr)
		require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
	})

	t.Run("while reading a payload", func(t *testing.T) {
		cut := segment.HeaderSize + segment.RecordHeaderSize + 1
		_, err := segment.ScanAll(&failingReader{data: full[:cut], err: ioErr}, sealedOpts())
		require.ErrorIs(t, err, ioErr)
		require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
	})
}

// TestScanFile_RefusesNonRegularFiles keeps ScanFile from opening
// anything that is not a file with contents (ADR-005).
func TestScanFile_RefusesNonRegularFiles(t *testing.T) {
	_, err := segment.ScanFile(t.TempDir(), sealedOpts())
	require.ErrorIs(t, err, segment.ErrForeignEntry)
}

// TestScanFile_UnreadableSegmentReportsTheMediumError separates a
// permission problem from damage.
func TestScanFile_UnreadableSegmentReportsTheMediumError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, segment.FileName(1))
	require.NoError(t, os.WriteFile(path, buildSegment(t, testHeader(), testKey, "one"), 0o600))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := segment.ScanFile(path, sealedOpts())
	require.Error(t, err)
	require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
}

// TestScanner_StopsForGood pins the no-resume contract directly on the
// Scanner: once a scan has stopped — at the end, at a torn tail, or on
// a verdict — calling Next again must not deliver anything further.
// This is what makes "never skip" hold for a caller that keeps looping,
// rather than only for the ones that check Err promptly.
func TestScanner_StopsForGood(t *testing.T) {
	h := testHeader()
	full := buildSegment(t, h, testKey, "one", "two", "three")
	oneRecord := buildSegment(t, h, testKey, "one")

	tests := []struct {
		name      string
		data      []byte
		opts      segment.ScanOptions
		wantRecs  int
		wantErr   error
		truncated bool
	}{
		{"clean end of segment", full, sealedOpts(), 3, nil, false},
		{"torn tail on an active segment", full[:len(full)-3], activeOpts(), 2, nil, true},
		{"damage in a sealed segment", func() []byte {
			b := bytes.Clone(full)
			b[len(oneRecord)+20] ^= 0x01
			return b
		}(), sealedOpts(), 1, segment.ErrSegmentCorrupt, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := segment.NewScanner(bytes.NewReader(tt.data), tt.opts)
			require.NoError(t, err)

			got := 0
			for sc.Next() {
				got++
			}
			require.Equal(t, tt.wantRecs, got)

			// Every further call must stay false, and neither the
			// verdict nor the truncation flag may be reset by it.
			for i := 0; i < 3; i++ {
				require.False(t, sc.Next(), "Next must stay false after the scan stops")
			}
			if tt.wantErr != nil {
				require.ErrorIs(t, sc.Err(), tt.wantErr)
			} else {
				require.NoError(t, sc.Err())
			}
			require.Equal(t, tt.truncated, sc.Truncated())
		})
	}
}
