// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// shortHeaderCuts is every byte at which a kill -9 during segment creation
// can leave the file: 0 through HeaderSize-1 (MTIX-302).
var shortHeaderCuts = func() []int {
	cuts := make([]int, segment.HeaderSize)
	for i := range cuts {
		cuts[i] = i
	}
	return cuts
}()

// publishTwoThenCrashSegment publishes rs 1..2 into segment 1, then leaves
// segment 2 holding only the first cut bytes of a valid header.
func publishTwoThenCrashSegment(t *testing.T, dir string, cut int) []byte {
	t.Helper()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	for _, p := range []string{"one", "two"} {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Rotate())
	require.NoError(t, w.Close())
	second := filepath.Join(dir, segment.FileName(2))
	full, err := os.ReadFile(second)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(second, full[:cut], 0o600))
	first, err := os.ReadFile(filepath.Join(dir, segment.FileName(1)))
	require.NoError(t, err)
	return first
}

// TestWriter_NewestSegmentWithIncompleteHeader_IsReplacedAtEveryCut covers
// the crash during a rotation: the previous segment stays byte-identical and
// the header-only successor is replaced under the same segment number.
func TestWriter_NewestSegmentWithIncompleteHeader_IsReplacedAtEveryCut(t *testing.T) {
	for _, cut := range shortHeaderCuts {
		t.Run(fmt.Sprintf("cut_%d", cut), func(t *testing.T) {
			dir := t.TempDir()
			firstBytes := publishTwoThenCrashSegment(t, dir, cut)

			w, err := segment.NewWriter(writerConfig(dir))
			require.NoError(t, err)
			require.Equal(t, uint64(2), w.SegmentNo())
			require.Equal(t, uint64(3), w.NextRS())
			rs, err := w.Append([]byte("three"))
			require.NoError(t, err)
			require.Equal(t, uint64(3), rs)
			require.NoError(t, w.Close())

			payloads, headers := readSegments(t, dir, testKey)
			require.Equal(t, []string{"one", "two", "three"}, payloads)
			require.Len(t, headers, 2)
			require.Equal(t, uint64(3), headers[1].FirstRS)
			after, err := os.ReadFile(filepath.Join(dir, segment.FileName(1)))
			require.NoError(t, err)
			require.Equal(t, firstBytes, after, "a segment holding records is never touched")
		})
	}
}

// TestWriter_OnlySegmentWithIncompleteHeader_StartsTheStreamAgain covers the
// kill during a peer's very first segment.
func TestWriter_OnlySegmentWithIncompleteHeader_StartsTheStreamAgain(t *testing.T) {
	for _, cut := range shortHeaderCuts {
		t.Run(fmt.Sprintf("cut_%d", cut), func(t *testing.T) {
			dir := t.TempDir()
			w, err := segment.NewWriter(writerConfig(dir))
			require.NoError(t, err)
			require.NoError(t, w.Close())
			path := filepath.Join(dir, segment.FileName(1))
			full, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, full[:cut], 0o600))

			w2, err := segment.NewWriter(writerConfig(dir))
			require.NoError(t, err)
			require.Equal(t, uint64(1), w2.SegmentNo())
			require.Equal(t, uint64(1), w2.NextRS())
			_, err = w2.Append([]byte("one"))
			require.NoError(t, err)
			require.NoError(t, w2.Close())
			payloads, _ := readSegments(t, dir, testKey)
			require.Equal(t, []string{"one"}, payloads)
		})
	}
}

// TestWriter_IncompleteHeaderRecovery_KeepsTheDeclaredCursorSelfCheck proves
// recovery does not weaken FR-21 §5.2: a cursor that disagrees with the
// medium's last whole record is still refused.
func TestWriter_IncompleteHeaderRecovery_KeepsTheDeclaredCursorSelfCheck(t *testing.T) {
	dir := t.TempDir()
	publishTwoThenCrashSegment(t, dir, 46)

	cfg := writerConfig(dir)
	cfg.NextRS = 9
	_, err := segment.NewWriter(cfg)
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)

	cfg.NextRS = 3
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)
	require.NoError(t, w.Close())
}

// TestWriter_IncompleteHeaderRecovery_HonoursAnEpochBump covers a restore
// reset whose first publish was killed: the replacement header carries the
// declared epoch and base relay sequence.
func TestWriter_IncompleteHeaderRecovery_HonoursAnEpochBump(t *testing.T) {
	dir := t.TempDir()
	publishTwoThenCrashSegment(t, dir, 1)

	cfg := writerConfig(dir)
	cfg.PubEpoch = 2
	cfg.NextRS = 7
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)
	require.Equal(t, uint64(2), w.SegmentNo())
	require.Equal(t, uint64(7), w.NextRS())
	require.NoError(t, w.Close())
	_, headers := readSegments(t, dir, testKey)
	require.Equal(t, uint16(2), headers[1].PubEpoch)
	require.Equal(t, uint64(7), headers[1].FirstRS)
}

// TestWriter_IncompleteHeaderOnASealedSegment_StaysCorrupt pins the MTIX-288
// verdict: only the newest segment is replaceable; a short header with a
// successor is damage and the writer refuses to publish past it.
func TestWriter_IncompleteHeaderOnASealedSegment_StaysCorrupt(t *testing.T) {
	dir := t.TempDir()
	firstBytes := publishTwoThenCrashSegment(t, dir, 46)
	second := filepath.Join(dir, segment.FileName(2))
	secondBytes, err := os.ReadFile(second)
	require.NoError(t, err)
	// A magic-prefixed leftover passes the crash predicate, so only the
	// newest-segment-only rule can refuse it.
	third := filepath.Join(dir, segment.FileName(3))
	require.NoError(t, os.WriteFile(third, []byte("MR"), 0o600))
	// Segment 2 is now sealed (a successor exists) and short; segment 3 is short too.
	_, err = segment.NewWriter(writerConfig(dir))
	require.Error(t, err)
	require.ErrorIs(t, err, segment.ErrIncomplete, "two short headers are not one crash")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Len(t, entries, 3, "nothing is removed or created")
	for path, want := range map[string][]byte{
		filepath.Join(dir, segment.FileName(1)): firstBytes,
		second:                                  secondBytes,
		third:                                   []byte("MR"),
	} {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, want, got, "no segment is rewritten")
	}
}

// TestWriter_ShortForeignContentUnderASegmentName_IsRefused guards the
// replacement: only a prefix of a real header is a crash leftover, so short
// foreign data is never overwritten.
func TestWriter_ShortForeignContentUnderASegmentName_IsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, segment.FileName(1))
	require.NoError(t, os.WriteFile(path, []byte("operator notes, not a header"), 0o600))
	_, err := segment.NewWriter(writerConfig(dir))
	require.ErrorIs(t, err, segment.ErrIncomplete)
	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "operator notes, not a header", string(got))
}

// TestIsCrashedHeader_TableByContent pins the crash-leftover predicate.
func TestIsCrashedHeader_TableByContent(t *testing.T) {
	hdr := []byte(segment.MagicSegment + "\x01rest-of-header-bytes")
	tests := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"empty", nil, true},
		{"one magic byte", []byte("M"), true},
		{"partial magic", []byte("MR"), true},
		{"past the magic", hdr, true},
		{"foreign short", []byte("hello"), false},
		{"wrong first byte", []byte("X"), false},
		{"whole length", make([]byte, segment.HeaderSize), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f")
			require.NoError(t, os.WriteFile(path, tt.content, 0o600))
			got, err := segment.IsCrashedHeader(path)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	_, err := segment.IsCrashedHeader(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
