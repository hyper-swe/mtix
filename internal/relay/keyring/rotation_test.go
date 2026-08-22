// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package keyring_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

const rotationPeerID = "0123456789abcdef"

// rotatedStream publishes across a key rotation the way the operator
// command will: records under the old epoch, then `relay rotate-key`
// installs the next epoch and the publisher opens a fresh segment for
// it. Every record in a file therefore shares one key epoch — the
// segment layer already refuses to mix them.
//
// It returns the segment directory and the payloads published, in
// order, with the index at which the boundary falls.
func rotatedStream(t *testing.T, keysDir string, oldKey, newKey []byte) (segDir string, published []string, boundary int) {
	t.Helper()
	segDir = filepath.Join(t.TempDir(), "segments")
	require.NoError(t, os.MkdirAll(segDir, 0o700))
	require.NoError(t, keyring.Write(keysDir, 1, oldKey))

	w, err := segment.NewWriter(segment.WriterConfig{
		Dir: segDir, PeerID: rotationPeerID, Key: oldKey, KeyEpoch: 1, PubEpoch: 1,
	})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		p := fmt.Sprintf(`{"event_id":"pre-%d"}`, i)
		_, err := w.Append([]byte(p))
		require.NoError(t, err)
		published = append(published, p)
	}
	require.NoError(t, w.Close())
	boundary = len(published)

	// The rotation: a new epoch key is installed, and the publisher
	// comes back under it.
	require.NoError(t, keyring.Write(keysDir, 2, newKey))
	w2, err := segment.NewWriter(segment.WriterConfig{
		Dir: segDir, PeerID: rotationPeerID, Key: newKey, KeyEpoch: 2, PubEpoch: 1,
	})
	require.NoError(t, err)
	require.Greater(t, w2.SegmentNo(), uint64(1), "a key epoch change opens a fresh segment")
	for i := 0; i < 3; i++ {
		p := fmt.Sprintf(`{"event_id":"post-%d"}`, i)
		_, err := w2.Append([]byte(p))
		require.NoError(t, err)
		published = append(published, p)
	}
	require.NoError(t, w2.Close())
	return segDir, published, boundary
}

// readWithRing walks every segment, selecting the key by the epoch the
// frame declares — the reader-side rule this package exists to
// implement. It returns the payloads read and the first verdict.
func readWithRing(t *testing.T, segDir string, ring *keyring.Ring) ([]string, error) {
	t.Helper()
	segs, foreign, err := segment.ListSegments(segDir)
	require.NoError(t, err)
	require.Empty(t, foreign)

	var out []string
	for i, s := range segs {
		// The header must be readable before a key can be chosen, so
		// peek it unauthenticated first — the header carries no MAC.
		peek, err := os.ReadFile(s.Path)
		require.NoError(t, err)
		h, err := segment.ParseHeader(peek)
		require.NoError(t, err)

		key, err := ring.For(h.KeyEpoch)
		if err != nil {
			return out, err
		}
		res, err := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segs)-1, Key: key, ExpectPeerID: rotationPeerID,
		})
		if err != nil {
			return out, err
		}
		for _, rec := range res.Records {
			out = append(out, string(rec.Payload))
		}
	}
	return out, nil
}

// TestRotation_BothEpochsLiveReadsTheWholeStream is D-R10's positive
// half: with the pre-boundary key still installed, a reader crossing a
// rotation reads every record on both sides of it. Rotation is proved
// from the frames alone — nothing here consults relay.json or compares
// relay sequences against mutable metadata to decide which key to use.
func TestRotation_BothEpochsLiveReadsTheWholeStream(t *testing.T) {
	keysDir := filepath.Join(t.TempDir(), "keys")
	oldKey, newKey := testKeyN(0xa1), testKeyN(0xb2)
	segDir, published, boundary := rotatedStream(t, keysDir, oldKey, newKey)

	ring, err := keyring.Load(keysDir)
	require.NoError(t, err)
	require.Equal(t, []uint16{1, 2}, ring.Epochs())

	got, err := readWithRing(t, segDir, ring)
	require.NoError(t, err)
	require.Equal(t, published, got)
	require.Greater(t, len(published), boundary, "the stream must span the boundary")
}

// TestRotation_OldKeyValidVersusWrongKeyForged is the distinction D-R10
// exists to make, and the reason the pre-boundary key stays installed.
//
// Three readers meet the identical pre-boundary bytes:
//
//   - the right key for that epoch  => the records verify
//   - a wrong key for that epoch    => RELAY_SEGMENT_CORRUPT (forged)
//   - no key for that epoch at all  => RELAY_KEY_ABSENT (operator gap)
//
// Collapsing the last two is the failure mode worth spending a test on:
// an operator who pruned an old key too early would be told their
// history had been tampered with, and would go hunting an attacker who
// was never there.
func TestRotation_OldKeyValidVersusWrongKeyForged(t *testing.T) {
	oldKey, newKey, wrongKey := testKeyN(0xa1), testKeyN(0xb2), testKeyN(0xcc)

	t.Run("the right key verifies the pre-boundary records", func(t *testing.T) {
		keysDir := filepath.Join(t.TempDir(), "keys")
		segDir, published, _ := rotatedStream(t, keysDir, oldKey, newKey)

		ring, err := keyring.Load(keysDir)
		require.NoError(t, err)
		got, err := readWithRing(t, segDir, ring)
		require.NoError(t, err)
		require.Equal(t, published, got)
	})

	t.Run("a wrong key at that epoch reads as forged", func(t *testing.T) {
		keysDir := filepath.Join(t.TempDir(), "keys")
		segDir, _, _ := rotatedStream(t, keysDir, oldKey, newKey)

		// Same epoch numbers, wrong material for epoch 1.
		swapped := filepath.Join(t.TempDir(), "keys")
		require.NoError(t, keyring.Write(swapped, 1, wrongKey))
		require.NoError(t, keyring.Write(swapped, 2, newKey))
		ring, err := keyring.Load(swapped)
		require.NoError(t, err)

		got, err := readWithRing(t, segDir, ring)
		require.ErrorIs(t, err, segment.ErrMACMismatch)
		require.Equal(t, "RELAY_SEGMENT_CORRUPT", segment.CodeOf(err))
		require.Empty(t, got, "not one record may be delivered under a wrong key")
	})

	t.Run("no key at that epoch reads as an operator gap", func(t *testing.T) {
		keysDir := filepath.Join(t.TempDir(), "keys")
		segDir, _, _ := rotatedStream(t, keysDir, oldKey, newKey)

		// The pre-boundary key was pruned too early.
		pruned := filepath.Join(t.TempDir(), "keys")
		require.NoError(t, keyring.Write(pruned, 2, newKey))
		ring, err := keyring.Load(pruned)
		require.NoError(t, err)

		got, err := readWithRing(t, segDir, ring)
		require.ErrorIs(t, err, keyring.ErrKeyAbsent)
		require.Equal(t, "RELAY_KEY_ABSENT", keyring.CodeOf(err))
		require.NotEqual(t, "RELAY_SEGMENT_CORRUPT", segment.CodeOf(err),
			"a missing key must never be reported as tampering")
		require.Empty(t, got)
	})
}

// TestRotation_RecordsDoNotTravelAcrossTheEpochBoundary confirms the
// frame binding holds across a rotation specifically: a genuine
// pre-boundary record replayed into the post-boundary epoch fails
// authentication under either key. The epoch is in the MAC, so the
// boundary is not a place an attacker can splice at.
func TestRotation_RecordsDoNotTravelAcrossTheEpochBoundary(t *testing.T) {
	oldKey, newKey := testKeyN(0xa1), testKeyN(0xb2)
	keysDir := filepath.Join(t.TempDir(), "keys")
	segDir, _, _ := rotatedStream(t, keysDir, oldKey, newKey)

	segs, _, err := segment.ListSegments(segDir)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(segs), 2)

	preRaw, err := os.ReadFile(segs[0].Path)
	require.NoError(t, err)
	preHeader, err := segment.ParseHeader(preRaw)
	require.NoError(t, err)
	require.Equal(t, uint16(1), preHeader.KeyEpoch)

	rec, _, err := segment.ParseRecord(preRaw[segment.HeaderSize:], preHeader, oldKey)
	require.NoError(t, err)

	// Re-read that record's bytes in the post-boundary context.
	postRaw, err := os.ReadFile(segs[len(segs)-1].Path)
	require.NoError(t, err)
	postHeader, err := segment.ParseHeader(postRaw)
	require.NoError(t, err)
	require.Equal(t, uint16(2), postHeader.KeyEpoch)

	frame := preRaw[segment.HeaderSize : segment.HeaderSize+segment.RecordHeaderSize+len(rec.Payload)]
	for _, tt := range []struct {
		name string
		key  []byte
	}{
		{"under the new key", newKey},
		{"under the old key", oldKey},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := segment.ParseRecord(frame, postHeader, tt.key)
			require.ErrorIs(t, err, segment.ErrMACMismatch)
		})
	}
}

// TestRotation_PublisherWritesUnderTheCurrentEpoch pins the other side
// of the selection rule: a publisher uses the highest epoch installed,
// so installing a key is what makes a rotation take effect.
func TestRotation_PublisherWritesUnderTheCurrentEpoch(t *testing.T) {
	keysDir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, keyring.Write(keysDir, 1, testKeyN(0xa1)))
	require.NoError(t, keyring.Write(keysDir, 4, testKeyN(0xb2)))
	require.NoError(t, keyring.Write(keysDir, 2, testKeyN(0xc3)))

	ring, err := keyring.Load(keysDir)
	require.NoError(t, err)
	epoch, key, err := ring.Current()
	require.NoError(t, err)
	require.Equal(t, uint16(4), epoch)

	segDir := filepath.Join(t.TempDir(), "segments")
	require.NoError(t, os.MkdirAll(segDir, 0o700))
	w, err := segment.NewWriter(segment.WriterConfig{
		Dir: segDir, PeerID: rotationPeerID, Key: key, KeyEpoch: epoch, PubEpoch: 1,
	})
	require.NoError(t, err)
	_, err = w.Append([]byte(`{"event_id":"x"}`))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	got, err := readWithRing(t, segDir, ring)
	require.NoError(t, err)
	require.Equal(t, []string{`{"event_id":"x"}`}, got)
}
