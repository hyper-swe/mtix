// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

func writerConfig(dir string) segment.WriterConfig {
	return segment.WriterConfig{
		Dir:      dir,
		PeerID:   testPeerID,
		Key:      testKey,
		KeyEpoch: 1,
		PubEpoch: 1,
	}
}

// readSegments scans every segment in dir, sealing all but the last,
// and returns the payloads in stream order together with the headers.
func readSegments(t *testing.T, dir string, key []byte) ([]string, []segment.Header) {
	t.Helper()
	segs, foreign, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Empty(t, foreign)

	var payloads []string
	var headers []segment.Header
	for i, s := range segs {
		res, err := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed:       i < len(segs)-1,
			Key:          key,
			ExpectPeerID: testPeerID,
		})
		require.NoError(t, err, "segment %d", s.No)
		headers = append(headers, res.Header)
		payloads = append(payloads, payloadsOf(res.Records)...)
	}
	return payloads, headers
}

// TestWriter_FirstSegmentStartsTheStream covers a peer's first publish.
func TestWriter_FirstSegmentStartsTheStream(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	require.Equal(t, uint64(1), w.SegmentNo())
	require.Equal(t, uint64(1), w.NextRS())
	require.False(t, w.RecoveredFromTornTail())

	for i, p := range []string{"one", "two", "three"} {
		rs, err := w.Append([]byte(p))
		require.NoError(t, err)
		require.Equal(t, uint64(i+1), rs)
	}
	require.NoError(t, w.Close())

	payloads, headers := readSegments(t, dir, testKey)
	require.Equal(t, []string{"one", "two", "three"}, payloads)
	require.Len(t, headers, 1)
	require.Equal(t, uint64(1), headers[0].SegmentNo)
	require.Equal(t, uint64(1), headers[0].FirstRS)
	require.Equal(t, uint16(1), headers[0].KeyEpoch)
	require.Equal(t, uint16(1), headers[0].PubEpoch)
	require.True(t, headers[0].Authenticated())
}

// TestWriter_ResumesAnExistingStream is the ordinary restart: a
// publisher that comes back finds its own tail and continues it.
func TestWriter_ResumesAnExistingStream(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	_, err = w.Append([]byte("one"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	w2, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	require.Equal(t, uint64(1), w2.SegmentNo(), "an intact tail is continued, not rotated away")
	require.Equal(t, uint64(2), w2.NextRS())
	require.False(t, w2.RecoveredFromTornTail())
	rs, err := w2.Append([]byte("two"))
	require.NoError(t, err)
	require.Equal(t, uint64(2), rs)
	require.NoError(t, w2.Close())

	payloads, headers := readSegments(t, dir, testKey)
	require.Equal(t, []string{"one", "two"}, payloads)
	require.Len(t, headers, 1)
}

// TestWriter_RotatesPastTheSizeLimit is FR-21 §5.3: rotation is the
// only commit primitive the design needs, and relay sequences continue
// across the boundary so a reader can check contiguity through it.
func TestWriter_RotatesPastTheSizeLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := writerConfig(dir)
	cfg.MaxSegmentBytes = int64(segment.HeaderSize + 2*(segment.RecordHeaderSize+8))
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := w.Append(bytes.Repeat([]byte("x"), 8))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	segs, _, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Len(t, segs, 3, "five records at two per segment")

	_, headers := readSegments(t, dir, testKey)
	require.Equal(t, []uint64{1, 2, 3}, []uint64{headers[0].SegmentNo, headers[1].SegmentNo, headers[2].SegmentNo})
	require.Equal(t, []uint64{1, 3, 5}, []uint64{headers[0].FirstRS, headers[1].FirstRS, headers[2].FirstRS})

	// Every boundary must satisfy the reader's cross-segment rule.
	prev := segment.Cursor{}
	for i, h := range headers {
		require.NoError(t, segment.CheckContinuity(prev, h), "boundary into segment %d", i+1)
		prev = segment.Cursor{SegmentNo: h.SegmentNo, RS: h.FirstRS + 1, PubEpoch: h.PubEpoch}
	}
}

// TestWriter_NeverSplitsARecordAcrossSegments keeps a record whole even
// when it alone exceeds the rotation threshold. Rotating forever on an
// oversized record would stall the stream instead of carrying it.
func TestWriter_NeverSplitsARecordAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	cfg := writerConfig(dir)
	cfg.MaxSegmentBytes = segment.HeaderSize + 1
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)

	big := bytes.Repeat([]byte("y"), 4096)
	for i := 0; i < 3; i++ {
		_, err := w.Append(big)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	segs, _, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Len(t, segs, 3, "one oversized record per segment, never a split one")
	payloads, _ := readSegments(t, dir, testKey)
	require.Len(t, payloads, 3)
	require.Equal(t, string(big), payloads[0])
}

// TestWriter_ExplicitRotate covers the operator/caller-driven boundary.
func TestWriter_ExplicitRotate(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)

	_, err = w.Append([]byte("one"))
	require.NoError(t, err)
	require.NoError(t, w.Rotate())
	require.Equal(t, uint64(2), w.SegmentNo())
	_, err = w.Append([]byte("two"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	payloads, headers := readSegments(t, dir, testKey)
	require.Equal(t, []string{"one", "two"}, payloads)
	require.Equal(t, uint64(2), headers[1].FirstRS)
}

// TestWriter_TornTailIsSealedNotRepaired is FR-21 §5.5, the rule that
// forbids rewrite-in-place. A publisher that crashed mid-append comes
// back to a partial frame it must not touch: on a bridged medium an
// in-place repair races a remote cache the writer cannot see, so the
// damaged segment is sealed by rotating past it and publishing
// continues in a fresh file.
func TestWriter_TornTailIsSealedNotRepaired(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	for _, p := range []string{"one", "two"} {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	// Simulate the crash: a partial third frame lands on the medium.
	path := filepath.Join(dir, segment.FileName(1))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.Write([]byte("MREC\x00\x00\x00\x09partial"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	w2, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	require.True(t, w2.RecoveredFromTornTail())
	require.Equal(t, uint64(2), w2.SegmentNo(), "recovery rotates rather than repairs")
	require.Equal(t, uint64(3), w2.NextRS(), "publishing resumes after the last whole record")

	rs, err := w2.Append([]byte("three"))
	require.NoError(t, err)
	require.Equal(t, uint64(3), rs)
	require.NoError(t, w2.Close())

	// The damaged bytes are still exactly where they were: recovery
	// never rewrote a byte of the sealed segment.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, append(bytes.Clone(before), []byte("MREC\x00\x00\x00\x09partial")...), after)
}

// TestWriter_RefusesADamagedHeader stops a publisher whose own newest
// segment is unreadable. There is no safe continuation to infer, and
// guessing one would publish under relay sequences a reader may already
// have consumed.
func TestWriter_RefusesADamagedHeader(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	_, err = w.Append([]byte("one"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	path := filepath.Join(dir, segment.FileName(1))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	copy(raw[0:4], "XXXX")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	_, err = segment.NewWriter(writerConfig(dir))
	require.ErrorIs(t, err, segment.ErrSegmentCorrupt)
}

// TestWriter_SelfCheckRefusesAForeignTail is the FR-21 §5.2 self-check.
// A configured peer id rests on the operator not running two live
// writers under it, so the writer verifies on every start that the
// newest segment in its own directory is its own continuation — and
// refuses before a byte is written when it is not.
func TestWriter_SelfCheckRefusesAForeignTail(t *testing.T) {
	dir := t.TempDir()
	other := writerConfig(dir)
	other.PeerID = "fedcba9876543210"
	w, err := segment.NewWriter(other)
	require.NoError(t, err)
	_, err = w.Append([]byte("not mine"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	_, err = segment.NewWriter(writerConfig(dir))
	require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	require.Equal(t, "RELAY_PEER_ID_CONFLICT", segment.CodeOf(err))
}

// TestWriter_SelfCheckAgainstADurableCursor is the other half of the
// §5.2 check: when the caller declares where its durable cursor sits,
// a medium that disagrees is refused rather than reconciled.
func TestWriter_SelfCheckAgainstADurableCursor(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	for _, p := range []string{"one", "two"} {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	t.Run("a matching cursor resumes", func(t *testing.T) {
		cfg := writerConfig(dir)
		cfg.NextRS = 3
		w, err := segment.NewWriter(cfg)
		require.NoError(t, err)
		require.Equal(t, uint64(3), w.NextRS())
		require.NoError(t, w.Close())
	})
	t.Run("a cursor behind the medium is refused", func(t *testing.T) {
		cfg := writerConfig(dir)
		cfg.NextRS = 2
		_, err := segment.NewWriter(cfg)
		require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	})
	t.Run("a cursor ahead of the medium is refused", func(t *testing.T) {
		cfg := writerConfig(dir)
		cfg.NextRS = 9
		_, err := segment.NewWriter(cfg)
		require.ErrorIs(t, err, segment.ErrPeerIDConflict)
	})
}

// TestWriter_EpochBumpsOpenAFreshSegment covers both epoch transitions.
// A publisher restore (§5.7) declares a new base and republishes into
// fresh segments; a key rotation (§5.3) starts a new segment so every
// record in a file shares one key epoch.
func TestWriter_EpochBumpsOpenAFreshSegment(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	for _, p := range []string{"one", "two"} {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	t.Run("a key epoch bump rotates and continues the sequence", func(t *testing.T) {
		cfg := writerConfig(dir)
		cfg.KeyEpoch = 2
		w, err := segment.NewWriter(cfg)
		require.NoError(t, err)
		require.Equal(t, uint64(2), w.SegmentNo())
		require.Equal(t, uint64(3), w.NextRS())
		require.NoError(t, w.Close())
	})

	t.Run("a publisher epoch bump needs a declared base", func(t *testing.T) {
		cfg := writerConfig(dir)
		cfg.PubEpoch = 2
		_, err := segment.NewWriter(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "declared")
	})

	t.Run("a publisher epoch bump restarts at the declared base", func(t *testing.T) {
		cfg := writerConfig(dir)
		cfg.PubEpoch = 2
		cfg.NextRS = 1
		w, err := segment.NewWriter(cfg)
		require.NoError(t, err)
		require.Equal(t, uint64(1), w.NextRS())
		rs, err := w.Append([]byte("republished"))
		require.NoError(t, err)
		require.Equal(t, uint64(1), rs)
		require.NoError(t, w.Close())

		segs, _, err := segment.ListSegments(dir)
		require.NoError(t, err)
		last := segs[len(segs)-1]
		res, err := segment.ScanFile(last.Path, segment.ScanOptions{Key: testKey})
		require.NoError(t, err)
		require.Equal(t, uint16(2), res.Header.PubEpoch)
		require.Equal(t, uint64(1), res.Header.FirstRS)
	})
}

// TestWriter_RejectsOversizePayload keeps the §5.3 cap at the writer.
func TestWriter_RejectsOversizePayload(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	_, err = w.Append(bytes.Repeat([]byte("z"), segment.MaxPayloadBytes+1))
	require.ErrorIs(t, err, segment.ErrPayloadTooLarge)

	// The refusal must not have consumed a relay sequence or written a
	// byte: a rejected payload leaves no hole in the stream.
	require.Equal(t, uint64(1), w.NextRS())
	rs, err := w.Append([]byte("fits"))
	require.NoError(t, err)
	require.Equal(t, uint64(1), rs)
}

// TestWriter_ConfigValidation refuses an unusable writer up front.
func TestWriter_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*segment.WriterConfig)
	}{
		{"missing directory", func(c *segment.WriterConfig) { c.Dir = filepath.Join(c.Dir, "absent") }},
		{"peer id off grammar", func(c *segment.WriterConfig) { c.PeerID = "NOPE" }},
		{"authenticated without a key", func(c *segment.WriterConfig) { c.Key = nil }},
		{"negative size limit", func(c *segment.WriterConfig) { c.MaxSegmentBytes = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := writerConfig(t.TempDir())
			tt.mutate(&cfg)
			_, err := segment.NewWriter(cfg)
			require.Error(t, err)
		})
	}
}

// TestWriter_UnauthenticatedRelay covers the §8.3 mode end to end.
func TestWriter_UnauthenticatedRelay(t *testing.T) {
	dir := t.TempDir()
	cfg := writerConfig(dir)
	cfg.Key = nil
	cfg.Unauthenticated = true

	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)
	_, err = w.Append([]byte("plain"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	payloads, headers := readSegments(t, dir, nil)
	require.Equal(t, []string{"plain"}, payloads)
	require.False(t, headers[0].Authenticated())
}

// TestWriter_RefusesToClobberAnExistingSegment guards the single-writer
// invariant at the file-creation boundary. It is a sanity check, not a
// correctness mechanism: exclusive creation is not reliable on every
// medium the relay targets, which is exactly why the design removes the
// need for cross-host mutual exclusion instead of attempting it.
func TestWriter_RefusesToClobberAnExistingSegment(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	_, err = w.Append([]byte("one"))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, segment.FileName(2)), []byte("squatter"), 0o600))
	err = w.Rotate()
	require.Error(t, err)
	require.NoError(t, w.Close())
}

// TestWriter_AppendAfterClose refuses to write through a closed writer.
func TestWriter_AppendAfterClose(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	_, err = w.Append([]byte("late"))
	require.Error(t, err)
	require.NoError(t, w.Close(), "Close must be idempotent")
}

// TestWriter_CursorTracksPublishedPosition exposes the position a
// caller persists as its durable push cursor.
func TestWriter_CursorTracksPublishedPosition(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	require.Equal(t, segment.Cursor{SegmentNo: 1, RS: 0, PubEpoch: 1}, w.Cursor(),
		"nothing published yet")

	_, err = w.Append([]byte("one"))
	require.NoError(t, err)
	require.Equal(t, segment.Cursor{SegmentNo: 1, RS: 1, PubEpoch: 1}, w.Cursor())

	require.NoError(t, w.Rotate())
	_, err = w.Append([]byte("two"))
	require.NoError(t, err)
	require.Equal(t, segment.Cursor{SegmentNo: 2, RS: 2, PubEpoch: 1}, w.Cursor())

	// The cursor a reader would derive from the medium must agree with
	// the one the writer reports.
	_, headers := readSegments(t, dir, testKey)
	require.Equal(t, w.Cursor().SegmentNo, headers[len(headers)-1].SegmentNo)
}

// TestWriter_RotateAfterClose refuses to open a successor through a
// closed writer.
func TestWriter_RotateAfterClose(t *testing.T) {
	w, err := segment.NewWriter(writerConfig(t.TempDir()))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.Error(t, w.Rotate())
}

// TestWriter_UnreadableTailReportsTheMediumError keeps a permission
// problem on the publisher's own directory distinct from damage.
func TestWriter_UnreadableTailReportsTheMediumError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	_, err = w.Append([]byte("one"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	path := filepath.Join(dir, segment.FileName(1))
	require.NoError(t, os.Chmod(path, 0o400))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err = segment.NewWriter(writerConfig(dir))
	require.Error(t, err)
	require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
}

// TestWriter_RefusesASymlinkInItsOwnDirectory applies the FR-21 §5.1
// refusal to the publish path. A peer's own directory is on the shared
// medium too, so a link planted there is as reachable as one in a
// directory it reads.
func TestWriter_RefusesASymlinkInItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(dir, segment.FileName(1))))

	_, err := segment.NewWriter(writerConfig(dir))
	require.ErrorIs(t, err, segment.ErrSymlink)
}

// TestWriter_UnwritableDirectoryFailsBeforePublishing reports a medium
// that cannot be written as itself. FR-21 §6.2 is explicit that a
// publish failure is a degraded transport, never a damaged store, so
// nothing here may masquerade as a corruption verdict.
func TestWriter_UnwritableDirectoryFailsBeforePublishing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := segment.NewWriter(writerConfig(dir))
	require.Error(t, err)
	require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
}

// TestWriter_AppendSurfacesARotationFailure keeps a blocked rotation
// from being mistaken for a successful publish: the record is not
// written, and the relay sequence is not consumed.
func TestWriter_AppendSurfacesARotationFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := writerConfig(dir)
	cfg.MaxSegmentBytes = segment.HeaderSize + 1
	w, err := segment.NewWriter(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	_, err = w.Append([]byte("one"))
	require.NoError(t, err)

	// Something else on the medium already holds the successor's name.
	require.NoError(t, os.WriteFile(filepath.Join(dir, segment.FileName(2)), []byte("squatter"), 0o600))

	_, err = w.Append([]byte("two"))
	require.Error(t, err)
	require.Equal(t, uint64(2), w.NextRS(), "a failed publish consumes no relay sequence")
}
