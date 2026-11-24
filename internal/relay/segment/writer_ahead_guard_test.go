// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// craftSegment writes segment segNo holding n records from firstRS at
// pubEpoch, bypassing the writer so a test can lay out a medium the
// writer itself would never produce (an rs gap, an overlap, a header
// with no records).
func craftSegment(t *testing.T, dir string, segNo, firstRS uint64, pubEpoch uint16, n int) {
	t.Helper()
	h := segment.Header{
		FormatVersion: segment.FormatVersion,
		Flags:         segment.FlagAuthenticated,
		PeerID:        testPeerID,
		SegmentNo:     segNo,
		FirstRS:       firstRS,
		KeyEpoch:      1,
		PubEpoch:      pubEpoch,
	}
	b, err := h.MarshalBinary()
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		rs := firstRS + uint64(i)
		b, err = segment.AppendRecord(b, h, rs, []byte(fmt.Sprintf("r%d", rs)), testKey)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, segment.FileName(segNo)), b, 0o600))
}

// TestWriter_AheadAdoption_Guards pins every check that stands between a
// medium that is ahead of the cursor and the caller's AdoptAhead hook.
// Each refusal must happen before the hook runs: a hook that vouches for
// whatever it is shown would otherwise adopt a second writer's records.
func TestWriter_AheadAdoption_Guards(t *testing.T) {
	tornMiddle := func(t *testing.T, dir string) {
		writeStream(t, dir, int64(segment.HeaderSize+segment.RecordHeaderSize+3), "e1", "e2", "e3", "e4")
		segs, _, err := segment.ListSegments(dir)
		require.NoError(t, err)
		require.Greater(t, len(segs), 3, "one record per segment")
		f, err := os.OpenFile(segs[1].Path, os.O_WRONLY|os.O_APPEND, 0o600)
		require.NoError(t, err)
		_, err = f.Write([]byte("MREC\x00\x00\x00\x09partial"))
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	tests := []struct {
		name      string
		build     func(t *testing.T, dir string)
		pubEpoch  uint16
		nextRS    uint64
		wantSeen  []uint64 // relay sequences shown to the hook; nil when refused
		wantNext  uint64   // the writer's NextRS after adoption
		wantInErr string
	}{
		{
			name:     "gap_equal_to_max_is_adopted",
			build:    func(t *testing.T, d string) { craftSegment(t, d, 1, 1, 1, segment.MaxAheadRecords) },
			pubEpoch: 1, nextRS: 1,
			wantSeen: rangeRS(1, segment.MaxAheadRecords), wantNext: segment.MaxAheadRecords + 1,
		},
		{
			name:     "gap_over_max_is_refused_unread",
			build:    func(t *testing.T, d string) { craftSegment(t, d, 1, 1, 1, segment.MaxAheadRecords+1) },
			pubEpoch: 1, nextRS: 1, wantInErr: "cursor expects rs 1, the medium continues at rs 4098",
		},
		{
			name:     "fewer_records_than_the_gap_is_refused",
			build:    func(t *testing.T, d string) { craftSegment(t, d, 1, 5, 1, 0) },
			pubEpoch: 1, nextRS: 3, wantInErr: "not contiguous",
		},
		{
			name: "overlapping_segments_with_the_right_count_are_refused",
			build: func(t *testing.T, d string) {
				craftSegment(t, d, 1, 3, 1, 1) // rs 3
				craftSegment(t, d, 2, 3, 1, 2) // rs 3, 4: the run is [3 3 4], three records for a gap of three
			},
			pubEpoch: 1, nextRS: 2, wantInErr: "not contiguous",
		},
		{
			name: "run_crossing_a_publisher_epoch_is_refused",
			build: func(t *testing.T, d string) {
				craftSegment(t, d, 1, 1, 1, 2) // epoch 1: rs 1, 2
				craftSegment(t, d, 2, 3, 2, 2) // epoch 2: rs 3, 4
			},
			pubEpoch: 2, nextRS: 2, wantInErr: "not contiguous",
		},
		{
			name: "run_within_one_publisher_epoch_spans_segments",
			build: func(t *testing.T, d string) {
				craftSegment(t, d, 1, 1, 2, 2)
				craftSegment(t, d, 2, 3, 2, 2)
			},
			pubEpoch: 2, nextRS: 2, wantSeen: []uint64{2, 3, 4}, wantNext: 5,
		},
		{
			name:     "sealed_earlier_segment_with_a_torn_tail_is_tolerated",
			build:    tornMiddle,
			pubEpoch: 1, nextRS: 2, wantSeen: []uint64{2, 3, 4}, wantNext: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.build(t, dir)
			var seen []uint64
			calls := 0
			cfg := writerConfig(dir)
			cfg.PubEpoch = tt.pubEpoch
			cfg.NextRS = tt.nextRS
			cfg.AdoptAhead = func(ahead []segment.Record) error {
				calls++
				for _, r := range ahead {
					seen = append(seen, r.RS)
				}
				return nil
			}
			w, err := segment.NewWriter(cfg)
			if tt.wantSeen == nil {
				require.ErrorIs(t, err, segment.ErrPeerIDConflict)
				require.Equal(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
				require.Contains(t, err.Error(), tt.wantInErr)
				require.Zero(t, calls, "the hook must not be asked to vouch for a run the writer already doubts")
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, tt.wantSeen, seen)
			require.Equal(t, tt.wantNext, w.NextRS())
			require.NoError(t, w.Close())
		})
	}
}

func rangeRS(from, n uint64) []uint64 {
	out := make([]uint64, 0, n)
	for i := uint64(0); i < n; i++ {
		out = append(out, from+i)
	}
	return out
}

// A failure to verify, or to read the medium, says nothing about a
// second writer, so it must not be reported as RELAY_PEER_ID_CONFLICT.
func TestWriter_AheadAdoption_FailureToVerifyOrScan_IsNotAPeerIDConflict(t *testing.T) {
	boom := errors.New("disk on fire")
	tests := []struct {
		name  string
		build func(t *testing.T, dir string)
		adopt func([]segment.Record) error
	}{
		{
			name:  "hook_fails_to_read_its_journal",
			build: func(t *testing.T, d string) { writeStream(t, d, 0, "e1", "e2", "e3") },
			adopt: func([]segment.Record) error { return fmt.Errorf("read the journal: %w", boom) },
		},
		{
			name: "earlier_segment_does_not_scan",
			build: func(t *testing.T, d string) {
				writeStream(t, d, int64(segment.HeaderSize+segment.RecordHeaderSize+3), "e1", "e2", "e3", "e4")
				segs, _, err := segment.ListSegments(d)
				require.NoError(t, err)
				b, err := os.ReadFile(segs[1].Path)
				require.NoError(t, err)
				b[0] ^= 0xff // break the segment magic: not a torn tail, so a hard scan failure
				require.NoError(t, os.WriteFile(segs[1].Path, b, 0o600))
			},
			adopt: func([]segment.Record) error { return nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.build(t, dir)
			cfg := writerConfig(dir)
			cfg.NextRS = 2
			cfg.AdoptAhead = tt.adopt
			_, err := segment.NewWriter(cfg)
			require.Error(t, err)
			require.NotErrorIs(t, err, segment.ErrPeerIDConflict)
			require.NotEqual(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
		})
	}
}
