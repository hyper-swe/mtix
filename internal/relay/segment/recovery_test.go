// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// crashedPublisher builds the exact medium state FR-21 §5.5 describes:
// a publisher wrote some records, crashed mid-append leaving a partial
// frame, then restarted — sealing the damaged segment by rotating past
// it rather than repairing it — and published on.
//
// It returns the sealed segment's path, the successor's path, and the
// payloads that were published whole.
func crashedPublisher(t *testing.T, tornBytes []byte) (dir, sealed, successor string, published []string) {
	t.Helper()
	dir = t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	for _, p := range []string{"one", "two"} {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	sealed = filepath.Join(dir, segment.FileName(1))
	f, err := os.OpenFile(sealed, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.Write(tornBytes)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	w2, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	require.True(t, w2.RecoveredFromTornTail())
	_, err = w2.Append([]byte("three"))
	require.NoError(t, err)
	require.NoError(t, w2.Close())

	successor = filepath.Join(dir, segment.FileName(2))
	return dir, sealed, successor, []string{"one", "two", "three"}
}

// partialFrame is the first bytes of a real record frame — what a crash
// mid-append actually leaves behind.
func partialFrame(t *testing.T, rs uint64, cut int) []byte {
	t.Helper()
	full, err := segment.AppendRecord(nil, testHeader(), rs, []byte("interrupted"), testKey)
	require.NoError(t, err)
	return full[:cut]
}

// TestTornSeal_IsCorruptionUntilTheSuccessorExplainsIt is the FR-21
// §5.5 reader clause. A sealed segment with a torn tail is corruption
// by default — that default is what makes damage loud. It is exempt in
// exactly one case: the successor segment picks up where the sealed
// one's last whole record left off, so nothing was lost between them.
//
// Without this, every ordinary publisher crash would strand its readers
// on a corruption verdict that needs an operator, which would make the
// §5.5 recovery unusable in practice.
func TestTornSeal_IsCorruptionUntilTheSuccessorExplainsIt(t *testing.T) {
	_, sealed, successor, published := crashedPublisher(t, partialFrame(t, 3, 20))

	// The default read is unchanged: loud.
	_, err := segment.ScanFile(sealed, sealedOpts())
	require.ErrorIs(t, err, segment.ErrSegmentCorrupt)

	// The reconciliation read yields the clean prefix and the fact that
	// the tail is torn, without pronouncing on it.
	opts := sealedOpts()
	opts.TolerateTornTail = true
	sealedRes, err := segment.ScanFile(sealed, opts)
	require.NoError(t, err)
	require.True(t, sealedRes.Truncated)
	require.Equal(t, []string{"one", "two"}, payloadsOf(sealedRes.Records))

	succRes, err := segment.ScanFile(successor, sealedOpts())
	require.NoError(t, err)

	require.True(t, segment.ExplainsTornSeal(sealedRes, succRes.Header),
		"the successor continues from rs %d, so nothing was lost", sealedRes.Cursor.RS)

	// Reading the peer's stream through the reconciliation delivers
	// every record the publisher actually wrote.
	var applied []string
	applied = append(applied, payloadsOf(sealedRes.Records)...)
	applied = append(applied, payloadsOf(succRes.Records)...)
	require.Equal(t, published, applied)
}

// TestExplainsTornSeal_RefusesEverythingElse keeps the exemption narrow.
// Each case below is a way a torn seal could be waved through if the
// check were loose, and each one would silently drop a causal
// predecessor — the outcome §5.4 exists to prevent.
func TestExplainsTornSeal_RefusesEverythingElse(t *testing.T) {
	_, sealed, _, _ := crashedPublisher(t, partialFrame(t, 3, 20))
	opts := sealedOpts()
	opts.TolerateTornTail = true
	sealedRes, err := segment.ScanFile(sealed, opts)
	require.NoError(t, err)
	require.Equal(t, uint64(2), sealedRes.Cursor.RS)

	base := segment.Header{
		FormatVersion: segment.FormatVersion,
		Flags:         segment.FlagAuthenticated,
		PeerID:        testPeerID,
		SegmentNo:     2,
		FirstRS:       3,
		KeyEpoch:      1,
		PubEpoch:      1,
	}
	tests := []struct {
		name   string
		mutate func(*segment.Header)
	}{
		{"the successor skips a record", func(h *segment.Header) { h.FirstRS = 4 }},
		{"the successor skips many records", func(h *segment.Header) { h.FirstRS = 99 }},
		{"the successor is not the next segment", func(h *segment.Header) { h.SegmentNo = 3 }},
		{"the successor is an earlier segment", func(h *segment.Header) { h.SegmentNo = 1 }},
		{"the successor belongs to another peer", func(h *segment.Header) { h.PeerID = "fedcba9876543210" }},
		{"the successor is in another publisher epoch", func(h *segment.Header) { h.PubEpoch = 2 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := base
			tt.mutate(&h)
			require.False(t, segment.ExplainsTornSeal(sealedRes, h))
		})
	}

	t.Run("republished overlapping records are explained", func(t *testing.T) {
		// A publisher recovering from its durable cursor may re-emit
		// records the sealed segment already holds. Nothing is lost,
		// and the duplicates are absorbed downstream.
		h := base
		h.FirstRS = 1
		require.True(t, segment.ExplainsTornSeal(sealedRes, h))
	})

	t.Run("an intact sealed segment needs no exemption", func(t *testing.T) {
		intact, err := segment.ScanFile(sealed, sealedOpts())
		require.Error(t, err)
		_ = intact

		clean := segment.Result{
			Header:  base,
			Cursor:  segment.Cursor{SegmentNo: 2, RS: 3, PubEpoch: 1},
			Records: nil,
		}
		require.False(t, segment.ExplainsTornSeal(clean, base),
			"a segment that is not torn must never be waved through")
	})
}

// TestTornSeal_MidStreamDamageIsNotExplainedAway is the case the
// exemption must not swallow. Damage earlier than the tail loses
// records the successor does not replace, so the successor cannot
// continue from the clean prefix and the verdict stands.
func TestTornSeal_MidStreamDamageIsNotExplainedAway(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.NewWriter(writerConfig(dir))
	require.NoError(t, err)
	for _, p := range []string{"one", "two", "three"} {
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, w.Rotate())
	_, err = w.Append([]byte("four"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// Corrupt the second record of the now-sealed first segment.
	sealed := filepath.Join(dir, segment.FileName(1))
	raw, err := os.ReadFile(sealed)
	require.NoError(t, err)
	oneRecord := buildSegment(t, testHeader(), testKey, "one")
	raw[len(oneRecord)+20] ^= 0x01
	require.NoError(t, os.WriteFile(sealed, raw, 0o600))

	opts := sealedOpts()
	opts.TolerateTornTail = true
	sealedRes, err := segment.ScanFile(sealed, opts)
	require.NoError(t, err)
	require.Equal(t, []string{"one"}, payloadsOf(sealedRes.Records))

	succRes, err := segment.ScanFile(filepath.Join(dir, segment.FileName(2)), sealedOpts())
	require.NoError(t, err)

	require.False(t, segment.ExplainsTornSeal(sealedRes, succRes.Header),
		"records 2 and 3 are gone and the successor starts at 4 — this is real loss")
}

// TestTornSeal_TolerationCoversEveryCrashOffset walks the crash across
// every byte of an in-flight frame. A publisher can be killed anywhere,
// and none of those offsets may need an operator.
func TestTornSeal_TolerationCoversEveryCrashOffset(t *testing.T) {
	whole, err := segment.AppendRecord(nil, testHeader(), 3, []byte("interrupted"), testKey)
	require.NoError(t, err)

	for cut := 1; cut < len(whole); cut++ {
		_, sealed, successor, published := crashedPublisher(t, whole[:cut])

		opts := sealedOpts()
		opts.TolerateTornTail = true
		sealedRes, err := segment.ScanFile(sealed, opts)
		require.NoError(t, err, "crash at offset %d", cut)

		succRes, err := segment.ScanFile(successor, sealedOpts())
		require.NoError(t, err, "crash at offset %d", cut)
		require.True(t, segment.ExplainsTornSeal(sealedRes, succRes.Header),
			"crash at offset %d must be recoverable without an operator", cut)

		applied := append(payloadsOf(sealedRes.Records), payloadsOf(succRes.Records)...)
		require.Equal(t, published, applied, "crash at offset %d", cut)
	}
}
