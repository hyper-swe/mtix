// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

// TestRelayIngest_CloneFloorIsNotComparedAcrossPublisherEpochs: reset-peer
// restarts relay sequences at a low base under a new epoch (FR-21 5.7), so the
// clone position is only comparable within its own epoch (MTIX-265).
func TestRelayIngest_CloneFloorIsNotComparedAcrossPublisherEpochs(t *testing.T) {
	t.Run("clone taken in the new epoch is not stalled by an old-epoch segment with a high rs", func(t *testing.T) {
		r := newPeerRig(t)
		r.segNo, r.rs = 5, 401
		r.publishNodes(t, fleetKey, 401, 2) // epoch 1, rs 401-402, the oldest retained
		r.pubEpoch, r.rs = 2, 1
		r.publishNodes(t, fleetKey, 1, 2) // epoch 2, rs 1-2
		r.publishNodes(t, fleetKey, 3, 2) // epoch 2, rs 3-4
		r.seedCloneEpoch(t, 2, 2)

		require.Empty(t, r.doctorGaps(t), "doctor agrees")
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		require.Empty(t, stats.Stalls, "an old-epoch segment lies inside the snapshot")
		require.True(t, r.hasNode(t, "PROJ-4"))
		require.Equal(t, uint16(2), r.cursor(t).PubEpoch)
		require.Equal(t, uint64(4), r.cursor(t).RS)
	})

	t.Run("a hole inside the new epoch below the old epoch's rs is seen", func(t *testing.T) {
		r := newPeerRig(t)
		r.segNo, r.rs = 5, 399
		r.publishNodes(t, fleetKey, 399, 2) // epoch 1, rs 399-400, holds the clone position
		r.pubEpoch, r.rs = 2, 1
		r.publishNodes(t, fleetKey, 1, 2) // seg 6, epoch 2, rs 1-2
		r.publishNodes(t, fleetKey, 3, 2) // seg 7, deleted
		r.publishNodes(t, fleetKey, 5, 2) // seg 8, epoch 2, rs 5-6
		require.NoError(t, os.Remove(r.segPath(7)))
		r.seedCloneEpoch(t, 400, 1)

		require.Len(t, r.doctorGaps(t), 1, "doctor agrees")
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		s := gapStall(t, stats)
		require.Contains(t, s.Reason, "segment 8 does not follow segment 6")
		require.False(t, r.hasNode(t, "PROJ-5"), "nothing past the hole is applied")
	})
}

// failingKeys is a key ring that holds no key epoch.
type failingKeys struct{}

func (failingKeys) For(uint16) ([]byte, error) { return nil, errors.New("no such key epoch") }

// TestFindGaps_ReportsAWalkThatStoppedEarlyAsUnchecked: a segment the walk cannot
// read leaves what lies beyond it unknown, which must not read as a clean pass.
func TestFindGaps_ReportsAWalkThatStoppedEarlyAsUnchecked(t *testing.T) {
	t.Run("a damaged segment hides a gap behind it", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.publishNodes(t, fleetKey, 3, 2)
		r.publishNodes(t, fleetKey, 5, 2)
		r.publishNodes(t, fleetKey, 7, 2)
		require.NoError(t, os.WriteFile(r.segPath(2), []byte("not a segment"), 0o600))
		require.NoError(t, os.Remove(r.segPath(3))) // the gap beyond the damage
		rep := r.doctorReport(t)
		require.Empty(t, rep.Gaps)
		require.Len(t, rep.Unchecked, 1)
		require.Equal(t, remotePeer, rep.Unchecked[0].PeerID)
		require.Contains(t, rep.Unchecked[0].Reason, "segment 2")
	})

	t.Run("a key epoch this peer lacks", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer,
			func(string) (ingest.Position, error) { return ingest.Position{}, nil }, failingKeys{})
		require.NoError(t, err)
		require.Empty(t, rep.Gaps)
		require.Len(t, rep.Unchecked, 1)
		require.Contains(t, rep.Unchecked[0].Reason, "key epoch")
	})

	t.Run("a fully read stream is not unchecked", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.publishNodes(t, fleetKey, 3, 2)
		rep := r.doctorReport(t)
		require.Empty(t, rep.Gaps)
		require.Empty(t, rep.Unchecked)
	})
}

// TestFindGaps_HonoursCancellation: the walk reads every unread segment of every
// peer, so it must stop when its context is done.
func TestFindGaps_HonoursCancellation(t *testing.T) {
	r := newPeerRig(t)
	r.publishNodes(t, fleetKey, 1, 2)
	r.publishNodes(t, fleetKey, 3, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ingest.FindGaps(ctx, r.peersDir, selfPeer,
		func(string) (ingest.Position, error) { return ingest.Position{}, nil }, r.config().Keys)
	require.ErrorIs(t, err, context.Canceled)
}
