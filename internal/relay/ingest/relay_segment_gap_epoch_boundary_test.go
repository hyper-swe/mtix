// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

// TestRelayIngest_SegmentLostJustBeforeAnEpochChangeIsAGap: a publisher epoch
// change (reset-peer) starts in the very next segment, so a segment deleted
// right before it leaves a hole that the epoch change must not hide
// (MTIX-265). A legitimate reset, with no lost segment, still passes. Ingest and
// doctor must agree on every shape.
func TestRelayIngest_SegmentLostJustBeforeAnEpochChangeIsAGap(t *testing.T) {
	t.Run("fresh reader", func(t *testing.T) {
		build := func(t *testing.T, lose bool) *peerRig {
			r := newPeerRig(t)
			r.publishNodes(t, fleetKey, 1, 2) // seg 1, epoch 1, rs 1-2
			r.publishNodes(t, fleetKey, 3, 2) // seg 2, epoch 1, rs 3-4
			r.pubEpoch, r.rs = 2, 1
			r.publishNodes(t, fleetKey, 5, 2) // seg 3, epoch 2, rs 1-2
			if lose {
				require.NoError(t, os.Remove(r.segPath(2)))
			}
			return r
		}
		r := build(t, true)
		require.Len(t, r.doctorGaps(t), 1, "doctor sees the hole")
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		s := gapStall(t, stats)
		require.Contains(t, s.Reason, "must arrive in segment 2")
		require.False(t, r.hasNode(t, "PROJ-5"), "nothing past the hole is applied")

		ok := build(t, false)
		require.Empty(t, ok.doctorGaps(t))
		in, err = ingest.New(ok.config())
		require.NoError(t, err)
		stats, err = in.IngestAll(context.Background())
		require.NoError(t, err)
		require.Empty(t, stats.Stalls, "a legitimate epoch change with no lost segment passes")
		require.True(t, ok.hasNode(t, "PROJ-6"))
	})

	t.Run("clone-seeded reader", func(t *testing.T) {
		build := func(t *testing.T, lose bool) *peerRig {
			r := newPeerRig(t)
			r.segNo, r.rs, r.pubEpoch = 4, 1, 2
			r.publishNodes(t, fleetKey, 1, 2) // seg 4, epoch 2, rs 1-2, the clone position
			r.publishNodes(t, fleetKey, 3, 2) // seg 5, epoch 2, rs 3-4
			r.pubEpoch, r.rs = 3, 1
			r.publishNodes(t, fleetKey, 5, 2) // seg 6, epoch 3, rs 1-2
			if lose {
				require.NoError(t, os.Remove(r.segPath(5)))
			}
			r.seedCloneEpoch(t, 2, 2)
			return r
		}
		r := build(t, true)
		require.Len(t, r.doctorGaps(t), 1, "doctor sees the hole")
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		s := gapStall(t, stats)
		require.Contains(t, s.Reason, "must arrive in segment 5")
		require.False(t, r.hasNode(t, "PROJ-5"))

		ok := build(t, false)
		require.Empty(t, ok.doctorGaps(t))
		in, err = ingest.New(ok.config())
		require.NoError(t, err)
		stats, err = in.IngestAll(context.Background())
		require.NoError(t, err)
		require.Empty(t, stats.Stalls)
		require.True(t, ok.hasNode(t, "PROJ-6"))
	})
}

// TestRelayIngest_StampedEpochAtSequenceZeroSeedsTheCloneWalk: a snapshot taken
// after reset-peer and before the first publish stamps the new epoch at relay
// sequence 0. The older epoch lies inside the snapshot, so its oldest retained
// segment must not read as a gap to a fresh reader (MTIX-265).
func TestRelayIngest_StampedEpochAtSequenceZeroSeedsTheCloneWalk(t *testing.T) {
	r := newPeerRig(t)
	r.segNo, r.rs = 3, 41
	r.publishNodes(t, fleetKey, 41, 2) // seg 3, epoch 1, the oldest retained
	r.seedCloneEpoch(t, 0, 2)

	require.Empty(t, r.doctorGaps(t))
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Empty(t, stats.Stalls)
}
