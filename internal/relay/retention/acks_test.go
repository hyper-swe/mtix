// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package retention_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/retention"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// fakeCursors stands in for the store's durable ingest positions.
type fakeCursors struct {
	pos map[string]sqlite.RelayIngestPosition
	err error
}

func (f *fakeCursors) RelayIngestCursor(_ context.Context, peerID string) (sqlite.RelayIngestPosition, error) {
	if f.err != nil {
		return sqlite.RelayIngestPosition{}, f.err
	}
	return f.pos[peerID], nil
}

// TestAcks_RoundTrip covers the ordinary rewrite.
func TestAcks_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := retention.Acks{
		peerA: {SegmentNo: 3, RS: 51},
		peerB: {SegmentNo: 1, RS: 2},
	}
	require.NoError(t, retention.WriteAcks(dir, want))

	got, err := retention.ReadAcks(dir)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// TestAcks_MissingFileIsEmptyNotAnError: a peer that has never acked
// has an empty view, which blocks pruning rather than breaking it.
func TestAcks_MissingFileIsEmptyNotAnError(t *testing.T) {
	got, err := retention.ReadAcks(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestAcks_CorruptionIsReportedNotGuessed keeps a damaged ack file from
// being read as "this peer acked nothing" — which would be
// indistinguishable from a real position and would hold the window open
// silently. It is reported so the caller re-derives.
func TestAcks_CorruptionIsReportedNotGuessed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"truncated json", `{"0123456789abcdef":{"segment_no":1,`},
		{"not json", "this is not json"},
		{"a json array", `[]`},
		{"peer id off grammar", `{"NOT A PEER":{"segment_no":1,"rs":1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, retention.AcksFileName), []byte(tt.body), 0o600))
			_, err := retention.ReadAcks(dir)
			require.ErrorIs(t, err, retention.ErrAcksUnusable)
		})
	}
}

// TestAcks_SelfHealsFromTheStore is the FR-21 §6.7 advisory rule made
// concrete: the ack file is a convenience for other peers' pruning, not
// a record anything depends on. A corrupt file — or a rename the medium
// tore — is repaired from the reader's own durable positions, and the
// only cost of the damage is that writers pruned later than they could
// have.
func TestAcks_SelfHealsFromTheStore(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	src := &fakeCursors{pos: map[string]sqlite.RelayIngestPosition{
		peerA: {SegmentNo: 3, RS: 51, PubEpoch: 1},
		peerB: {SegmentNo: 1, RS: 2, PubEpoch: 1},
	}}
	peers := []string{peerA, peerB}

	// A torn rename left garbage behind.
	require.NoError(t, os.WriteFile(filepath.Join(dir, retention.AcksFileName),
		[]byte(`{"0123456789abcdef":{"segment_`), 0o600))

	acks, repaired, err := retention.ReconcileAcks(ctx, dir, peers, src)
	require.NoError(t, err)
	require.True(t, repaired, "an unusable file is rewritten, not tolerated")
	require.Equal(t, retention.Acks{
		peerA: {SegmentNo: 3, RS: 51},
		peerB: {SegmentNo: 1, RS: 2},
	}, acks)

	// And the repair is durable: the next read gets the true positions.
	onDisk, err := retention.ReadAcks(dir)
	require.NoError(t, err)
	require.Equal(t, acks, onDisk)

	t.Run("a healthy file is left alone", func(t *testing.T) {
		_, repaired, err := retention.ReconcileAcks(ctx, dir, peers, src)
		require.NoError(t, err)
		require.False(t, repaired)
	})

	t.Run("a file that fell behind the store is refreshed", func(t *testing.T) {
		src.pos[peerA] = sqlite.RelayIngestPosition{SegmentNo: 4, RS: 80, PubEpoch: 1}
		acks, repaired, err := retention.ReconcileAcks(ctx, dir, peers, src)
		require.NoError(t, err)
		require.True(t, repaired)
		require.Equal(t, uint64(80), acks[peerA].RS)
	})
}

// TestAcks_ReconcileReportsAStoreFailure keeps a database problem from
// being written into the ack file as a rewind — which other peers would
// read as this reader having lost ground, holding the window open.
func TestAcks_ReconcileReportsAStoreFailure(t *testing.T) {
	boom := errors.New("database is unavailable")
	_, _, err := retention.ReconcileAcks(context.Background(), t.TempDir(),
		[]string{peerA}, &fakeCursors{err: boom})
	require.ErrorIs(t, err, boom)
}

// TestAcks_WriteIsAtomicallyReplaced covers the temp-and-rename path,
// and — per ADR-005 — that nothing depends on the rename being atomic:
// a torn one leaves a file the reader repairs on its next pass.
func TestAcks_WriteIsAtomicallyReplaced(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, retention.WriteAcks(dir, retention.Acks{peerA: {SegmentNo: 1, RS: 1}}))
	require.NoError(t, retention.WriteAcks(dir, retention.Acks{peerA: {SegmentNo: 2, RS: 9}}))

	got, err := retention.ReadAcks(dir)
	require.NoError(t, err)
	require.Equal(t, uint64(9), got[peerA].RS)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp file is left behind")
}

// TestAcks_RefusesASymlink applies the CWE-59 discipline to the one file
// a reader writes into another peer's view.
func TestAcks_RefusesASymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "peer")
	require.NoError(t, os.Mkdir(dir, 0o700))
	target := filepath.Join(root, "elsewhere.json")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, retention.AcksFileName)))

	_, err := retention.ReadAcks(dir)
	require.ErrorIs(t, err, retention.ErrAcksUnusable)
}

// TestAcks_WriteRefusesAMalformedPeer keeps a bad id out of the file,
// where it would silently never match a real peer and hold the window
// open forever.
func TestAcks_WriteRefusesAMalformedPeer(t *testing.T) {
	require.Error(t, retention.WriteAcks(t.TempDir(), retention.Acks{"NOT A PEER": {RS: 1}}))
}

// TestAcks_WriteReportsAnUnwritableDirectory surfaces a medium problem
// as itself rather than leaving a half-written view.
func TestAcks_WriteReportsAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	require.Error(t, retention.WriteAcks(dir, retention.Acks{peerA: {RS: 1}}))
}

// TestAcks_ReadReportsAnUnreachableDirectory separates a permission
// problem on the path from a damaged file.
func TestAcks_ReadReportsAnUnreachableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "peer")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, retention.WriteAcks(dir, retention.Acks{peerA: {RS: 1}}))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	_, err := retention.ReadAcks(dir)
	require.Error(t, err)
	require.NotErrorIs(t, err, retention.ErrAcksUnusable,
		"an unreachable path is not a damaged file")
}

// TestAcks_ReadRefusesADirectoryInItsPlace covers a non-regular entry.
func TestAcks_ReadRefusesADirectoryInItsPlace(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, retention.AcksFileName), 0o700))
	_, err := retention.ReadAcks(dir)
	require.ErrorIs(t, err, retention.ErrAcksUnusable)
}

// TestAcks_ReconcileRepairsAShrinkingRoster covers the peer set changing
// between passes: a view naming a peer the relay no longer has is stale
// and is rewritten.
func TestAcks_ReconcileRepairsAShrinkingRoster(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	require.NoError(t, retention.WriteAcks(dir, retention.Acks{
		peerA: {SegmentNo: 1, RS: 1}, peerB: {SegmentNo: 1, RS: 1},
	}))

	src := &fakeCursors{pos: map[string]sqlite.RelayIngestPosition{
		peerA: {SegmentNo: 1, RS: 1},
	}}
	acks, repaired, err := retention.ReconcileAcks(ctx, dir, []string{peerA}, src)
	require.NoError(t, err)
	require.True(t, repaired)
	require.Len(t, acks, 1)
	require.NotContains(t, acks, peerB)
}

// TestAcks_ReconcileSurfacesMediumFailures covers the two ways the
// repair itself can fail. Neither may be mistaken for a damaged file:
// one is a path problem, the other a read-only medium, and both are
// retryable rather than requiring an operator.
func TestAcks_ReconcileSurfacesMediumFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	ctx := context.Background()
	src := &fakeCursors{pos: map[string]sqlite.RelayIngestPosition{peerA: {RS: 1}}}

	t.Run("the peer directory cannot be reached", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "peer")
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, retention.WriteAcks(dir, retention.Acks{peerA: {RS: 1}}))
		require.NoError(t, os.Chmod(root, 0o000))
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

		_, _, err := retention.ReconcileAcks(ctx, dir, []string{peerA}, src)
		require.Error(t, err)
		require.NotErrorIs(t, err, retention.ErrAcksUnusable)
	})

	t.Run("the repair cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		_, _, err := retention.ReconcileAcks(ctx, dir, []string{peerA}, src)
		require.Error(t, err)
	})
}
