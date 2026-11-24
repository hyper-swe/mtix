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

// writeStream publishes payloads through a writer rotating at maxBytes
// (zero: default) and closes it.
func writeStream(t *testing.T, dir string, maxBytes int64, payloads ...string) {
	t.Helper()
	cfg := writerConfig(dir)
	cfg.MaxSegmentBytes = maxBytes
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)
	for _, p := range payloads {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}

// adoptIf returns a verifier that records what it was shown and accepts
// the records exactly when they equal want.
func adoptIf(seen *[]string, want ...string) func([]segment.Record) error {
	return func(ahead []segment.Record) error {
		got := make([]string, 0, len(ahead))
		for _, r := range ahead {
			got = append(got, fmt.Sprintf("%d:%s", r.RS, r.Payload))
		}
		*seen = got
		if len(got) != len(want) {
			return errors.New("count differs")
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Errorf("record %d differs", i)
			}
		}
		return nil
	}
}

// TestWriter_MediumAheadOfCursor_AdoptedWhenCallerVouches covers the
// §6.2 crash window at the same pub_epoch: 1..n records past the cursor.
func TestWriter_MediumAheadOfCursor_AdoptedWhenCallerVouches(t *testing.T) {
	all := []string{"e1", "e2", "e3", "e4", "e5"}
	for n := 1; n <= 4; n++ {
		t.Run(fmt.Sprintf("ahead_by_%d", n), func(t *testing.T) {
			dir := t.TempDir()
			writeStream(t, dir, 0, all...)
			cursor := uint64(len(all) - n + 1) // next rs the caller expects
			var want []string
			for rs := cursor; rs <= uint64(len(all)); rs++ {
				want = append(want, fmt.Sprintf("%d:%s", rs, all[rs-1]))
			}
			var seen []string
			cfg := writerConfig(dir)
			cfg.NextRS = cursor
			cfg.AdoptAhead = adoptIf(&seen, want...)
			w, err := segment.NewWriter(cfg)
			require.NoError(t, err)
			require.Equal(t, want, seen)
			require.Equal(t, uint64(len(all)+1), w.NextRS(), "resumes after the adopted records")
			rs, err := w.Append([]byte("e6"))
			require.NoError(t, err)
			require.Equal(t, uint64(len(all)+1), rs)
			require.NoError(t, w.Close())
			got, _ := readSegments(t, dir, testKey)
			require.Equal(t, append(append([]string{}, all...), "e6"), got, "nothing republished or skipped")
		})
	}
}

func TestWriter_MediumAheadOfCursor_SpanningSegmentsAndATornTail(t *testing.T) {
	dir := t.TempDir()
	writeStream(t, dir, int64(segment.HeaderSize+segment.RecordHeaderSize+3), "e1", "e2", "e3", "e4")
	segs, _, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Greater(t, len(segs), 2, "records are spread over several segments")
	f, err := os.OpenFile(segs[len(segs)-1].Path, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.Write([]byte("MREC\x00\x00\x00\x09partial"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var seen []string
	cfg := writerConfig(dir)
	cfg.NextRS = 2
	cfg.AdoptAhead = adoptIf(&seen, "2:e2", "3:e3", "4:e4")
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"2:e2", "3:e3", "4:e4"}, seen)
	require.Equal(t, uint64(5), w.NextRS())
	require.NoError(t, w.Close())
}

func TestWriter_MediumAheadOfCursor_RefusedWhenCallerRejects(t *testing.T) {
	cases := map[string]func([]segment.Record) error{
		"no verifier": nil,
		"content mismatch": func([]segment.Record) error {
			return fmt.Errorf("%w: not the journal's next events", segment.ErrAheadMismatch)
		},
	}
	for name, adopt := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStream(t, dir, 0, "e1", "e2", "e3")
			path := filepath.Join(dir, segment.FileName(1))
			before, err := os.ReadFile(path)
			require.NoError(t, err)

			cfg := writerConfig(dir)
			cfg.NextRS = 2
			cfg.AdoptAhead = adopt
			_, err = segment.NewWriter(cfg)
			require.ErrorIs(t, err, segment.ErrPeerIDConflict)
			require.Equal(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
			require.Contains(t, err.Error(), "cursor expects rs 2, the medium continues at rs 4")

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "a refusal writes nothing")
		})
	}
}

// A medium behind the cursor lost data the caller believes published;
// no verifier can vouch for records that are not there.
func TestWriter_MediumBehindCursor_StillRefusedWithAVerifier(t *testing.T) {
	dir := t.TempDir()
	writeStream(t, dir, 0, "e1", "e2")
	cfg := writerConfig(dir)
	cfg.NextRS = 9
	cfg.AdoptAhead = func([]segment.Record) error { t.Fatal("verifier must not run"); return nil }
	_, err := segment.NewWriter(cfg)
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
}
