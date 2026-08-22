// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// generatedStream is a deterministically generated publisher stream:
// the segment files a writer produced, and the payloads it published,
// in order.
type generatedStream struct {
	dir      string
	segments []segment.File
	payloads []string
}

// generateStream publishes a deterministic mix of payload sizes through
// a real writer, rotating often enough to produce several segments.
//
// The generator is seeded fixed rather than randomly: a property test
// that fails only on some runs is a property test nobody can act on.
func generateStream(t *testing.T, records int) generatedStream {
	t.Helper()
	dir := t.TempDir()
	cfg := writerConfig(dir)
	cfg.MaxSegmentBytes = 1024
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)

	rng := rand.New(rand.NewPCG(0x5eed, 0x5e9))
	var payloads []string
	for i := 0; i < records; i++ {
		size := rng.IntN(200)
		p := fmt.Sprintf(`{"event_id":"%08d","body":%q}`, i, string(bytes.Repeat([]byte("e"), size)))
		if _, err := w.Append([]byte(p)); err != nil {
			require.NoError(t, err)
		}
		payloads = append(payloads, p)
	}
	require.NoError(t, w.Close())

	segs, foreign, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Empty(t, foreign)
	require.Greater(t, len(segs), 2, "the generated stream must span several segments")
	return generatedStream{dir: dir, segments: segs, payloads: payloads}
}

// requirePrefix asserts that got is a prefix of want, element for
// element. This is the property the whole layer exists to guarantee: a
// reader may see less than was published, never something else.
func requirePrefix(t *testing.T, want, got []string, ctx string) {
	t.Helper()
	require.LessOrEqual(t, len(got), len(want), "%s: delivered more records than exist", ctx)
	for i := range got {
		require.Equal(t, want[i], got[i], "%s: record %d differs from what was published", ctx, i)
	}
}

// segmentPayloads returns the payloads a segment holds, by scanning it
// whole as a sealed segment.
func segmentPayloads(t *testing.T, path string) []string {
	t.Helper()
	res, err := segment.ScanFile(path, segment.ScanOptions{Sealed: true, Key: testKey})
	require.NoError(t, err)
	return payloadsOf(res.Records)
}

// TestProperty_EveryBytePrefixIsACleanPrefixOrALoudVerdict is the FR-21
// §11.3 exit criterion for this deliverable, and the reason the frame
// layer can be trusted by everything above it.
//
// For every byte-prefix of a real generated segment — every offset,
// not a sample — reading it must produce either a clean prefix of the
// records that were published, or a loud verdict. It must never produce
// a record that differs from what the publisher wrote, and it must
// never produce a partial one. Both lifecycle states are checked,
// because the same bytes mean different things in each.
func TestProperty_EveryBytePrefixIsACleanPrefixOrALoudVerdict(t *testing.T) {
	stream := generateStream(t, 40)

	for _, seg := range stream.segments {
		full, err := os.ReadFile(seg.Path)
		require.NoError(t, err)
		want := segmentPayloads(t, seg.Path)

		for cut := 0; cut <= len(full); cut++ {
			ctx := fmt.Sprintf("segment %d cut at %d/%d", seg.No, cut, len(full))

			t.Run("active/"+ctx, func(t *testing.T) {
				res, err := segment.ScanAll(bytes.NewReader(full[:cut]), activeOpts())
				if err != nil {
					// The only loud verdict a truncation may produce on
					// an active segment is "the header is not here
					// yet" — everything past the header is a clean
					// stop by the §5.4 tail rule.
					require.True(t, segment.IsIncomplete(err), "%s: %v", ctx, err)
					require.Less(t, cut, segment.HeaderSize, ctx)
					return
				}
				requirePrefix(t, want, payloadsOf(res.Records), ctx)
				if res.Truncated {
					require.Less(t, len(res.Records), len(want),
						"%s: a truncated tail must mean records were withheld", ctx)
				}
				if cut == len(full) {
					require.False(t, res.Truncated, ctx)
					require.Equal(t, want, payloadsOf(res.Records), ctx)
				}
			})

			t.Run("sealed/"+ctx, func(t *testing.T) {
				res, err := segment.ScanAll(bytes.NewReader(full[:cut]), sealedOpts())
				if err != nil {
					require.ErrorIs(t, err, segment.ErrSegmentCorrupt, ctx)
					return
				}
				// A sealed segment that reads clean must be a whole
				// prefix: it ended on a frame boundary.
				requirePrefix(t, want, payloadsOf(res.Records), ctx)
			})
		}
	}
}

// TestProperty_TruncatingTheActiveTailNeverLosesHistory checks the
// property across the whole stream rather than one file: with the last
// segment truncated at every offset, walking every segment in order
// must yield a prefix of everything published, with contiguity intact
// through every boundary.
//
// This is the shape the ingest path actually runs in, so a property
// that held per-file but broke across the rotation boundary would be
// caught here.
func TestProperty_TruncatingTheActiveTailNeverLosesHistory(t *testing.T) {
	stream := generateStream(t, 40)
	last := stream.segments[len(stream.segments)-1]
	full, err := os.ReadFile(last.Path)
	require.NoError(t, err)

	for cut := 0; cut <= len(full); cut++ {
		require.NoError(t, os.WriteFile(last.Path, full[:cut], 0o600))

		var applied []string
		cursor := segment.Cursor{}
		for i, seg := range stream.segments {
			res, scanErr := segment.ScanFile(seg.Path, segment.ScanOptions{
				Sealed:       i < len(stream.segments)-1,
				Key:          testKey,
				ExpectPeerID: testPeerID,
			})
			if scanErr != nil {
				// A truncated active segment whose header has not
				// landed yet is the only stop a healthy stream can
				// produce here.
				require.True(t, segment.IsIncomplete(scanErr), "cut %d: %v", cut, scanErr)
				break
			}
			require.NoError(t, segment.CheckContinuity(cursor, res.Header),
				"cut %d: contiguity broke entering segment %d", cut, seg.No)
			applied = append(applied, payloadsOf(res.Records)...)
			cursor = res.Cursor
		}
		requirePrefix(t, stream.payloads, applied, fmt.Sprintf("stream truncated at %d", cut))
	}

	// Restored in full, the stream reads back exactly as published.
	require.NoError(t, os.WriteFile(last.Path, full, 0o600))
	var applied []string
	for i, seg := range stream.segments {
		res, err := segment.ScanFile(seg.Path, segment.ScanOptions{
			Sealed: i < len(stream.segments)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, err)
		applied = append(applied, payloadsOf(res.Records)...)
	}
	require.Equal(t, stream.payloads, applied)
}

// TestProperty_AnySingleBitFlipIsCaughtOrHarmless is the integrity half
// of the same guarantee. A sealed segment with one bit flipped anywhere
// — header, frame, checksum, tag, or payload — must either be refused
// or read back as a clean prefix of what was published. No flip may
// produce a record whose content differs from the publisher's.
//
// This is what makes the medium's behavior irrelevant to correctness:
// the reader validates content and never needs the medium's
// cooperation to know what is true.
func TestProperty_AnySingleBitFlipIsCaughtOrHarmless(t *testing.T) {
	stream := generateStream(t, 12)
	seg := stream.segments[0]
	full, err := os.ReadFile(seg.Path)
	require.NoError(t, err)
	want := segmentPayloads(t, seg.Path)

	scratch := filepath.Join(t.TempDir(), seg.Name)
	for i := 0; i < len(full); i++ {
		for _, bit := range []byte{0x01, 0x80} {
			flipped := bytes.Clone(full)
			flipped[i] ^= bit
			if bytes.Equal(flipped, full) {
				continue
			}
			require.NoError(t, os.WriteFile(scratch, flipped, 0o600))

			ctx := fmt.Sprintf("byte %d bit %#02x", i, bit)
			res, err := segment.ScanFile(scratch, sealedOpts())
			if err != nil {
				require.NotEmpty(t, segment.CodeOf(err), "%s: verdict must carry a code: %v", ctx, err)
				continue
			}
			requirePrefix(t, want, payloadsOf(res.Records), ctx)
		}
	}
}
