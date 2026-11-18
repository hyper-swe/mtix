// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

func (r *peerRig) seedClone(t *testing.T, rs uint64) { r.seedCloneEpoch(t, rs, 0) }

// seedCloneEpoch seeds the cursor as `relay clone` does from a snapshot that
// stamped a publisher epoch (zero is a snapshot that stamped none).
func (r *peerRig) seedCloneEpoch(t *testing.T, rs uint64, epoch uint16) {
	t.Helper()
	// The production seeding path, the one `relay clone` runs after the import.
	require.NoError(t, bootstrap.SeedCloneCursors(context.Background(), r.store, bootstrap.ImportResult{
		Positions: map[string]uint64{remotePeer: rs},
		Epochs:    map[string]uint16{remotePeer: epoch},
	}))
}

func (r *peerRig) doctorGaps(t *testing.T) []ingest.Gap { return r.doctorReport(t).Gaps }

func (r *peerRig) doctorReport(t *testing.T) ingest.GapReport {
	t.Helper()
	rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer, func(peer string) (ingest.Position, error) {
		p, err := r.store.RelayIngestCursor(context.Background(), peer)
		if err != nil {
			return ingest.Position{}, err
		}
		stamp, err := r.store.RelayCloneStampEpoch(context.Background(), peer)
		return ingest.StampedPosition(ingest.Position{SegmentNo: p.SegmentNo, RS: p.RS, PubEpoch: p.PubEpoch}, stamp), err
	}, r.config().Keys)
	require.NoError(t, err)
	return rep
}

// TestRelayIngest_HoleBelowTheClonePositionIsNotAGap: the snapshot already holds
// every event up to the clone position, so a segment missing below it is not a
// gap; only a hole above the position is. Ingest and doctor must both pass it.
func TestRelayIngest_HoleBelowTheClonePositionIsNotAGap(t *testing.T) {
	r := newPeerRig(t)
	r.publishNodes(t, fleetKey, 1, 2) // seg 1: rs 1-2
	r.publishNodes(t, fleetKey, 3, 2) // seg 2: rs 3-4, deleted
	r.publishNodes(t, fleetKey, 5, 2) // seg 3: rs 5-6, holds the position
	r.publishNodes(t, fleetKey, 7, 2) // seg 4: rs 7-8
	require.NoError(t, os.Remove(r.segPath(2)))
	r.seedClone(t, 6)

	require.Empty(t, r.doctorGaps(t), "doctor agrees: nothing above the clone position is missing")
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Empty(t, stats.Stalls)
	require.True(t, r.hasNode(t, "PROJ-7"))
	require.True(t, r.hasNode(t, "PROJ-8"))
	require.Equal(t, uint64(8), r.cursor(t).RS)
}

// TestRelayIngest_HoleAboveTheClonePositionAfterAHoleBelowStillStalls: the clone
// floor must not hide a hole that lies above the position.
func TestRelayIngest_HoleAboveTheClonePositionAfterAHoleBelowStillStalls(t *testing.T) {
	r := newPeerRig(t)
	r.publishNodes(t, fleetKey, 1, 2)
	r.publishNodes(t, fleetKey, 3, 2) // deleted, below the position
	r.publishNodes(t, fleetKey, 5, 2) // holds the position
	r.publishNodes(t, fleetKey, 7, 2) // deleted, above the position
	r.publishNodes(t, fleetKey, 9, 2)
	require.NoError(t, os.Remove(r.segPath(2)))
	require.NoError(t, os.Remove(r.segPath(4)))
	r.seedClone(t, 6)

	require.Len(t, r.doctorGaps(t), 1)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	s := gapStall(t, stats)
	require.Contains(t, s.Reason, "missing relay sequence 7 through 8")
	require.False(t, r.hasNode(t, "PROJ-9"))
}

// TestRelayIngest_ClonePositionBoundary pins the off-by-one at the earliest
// segment: it must start at the clone position + 1, no later (a later start
// skips an event silently) and no earlier requirement (a snapshot ending at a
// segment boundary must not stall).
func TestRelayIngest_ClonePositionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		clone   uint64
		stalled bool
	}{
		{"clone ends exactly before the segment", 10, false},
		{"clone overlaps the segment", 11, false},
		{"clone one event short of the segment", 9, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPeerRig(t)
			r.segNo, r.rs = 3, 11
			r.publishNodes(t, fleetKey, 11, 2) // rs 11-12
			r.seedClone(t, tc.clone)

			gaps := r.doctorGaps(t)
			in, err := ingest.New(r.config())
			require.NoError(t, err)
			stats, err := in.IngestAll(context.Background())
			require.NoError(t, err)
			if tc.stalled {
				s := gapStall(t, stats)
				require.Contains(t, s.Reason, "missing relay sequence "+strconv.Itoa(int(tc.clone)+1)+" through 10")
				require.Len(t, gaps, 1)
				require.False(t, r.hasNode(t, "PROJ-11"))
				return
			}
			require.Empty(t, stats.Stalls)
			require.Empty(t, gaps)
			require.True(t, r.hasNode(t, "PROJ-12"))
			require.Equal(t, uint64(12), r.cursor(t).RS)
		})
	}
}

// TestRelayIngest_DoctorAndIngestAgreeOnEveryGapShape: both walk a peer through
// the same chain, so for every shape either both refuse or neither does.
func TestRelayIngest_DoctorAndIngestAgreeOnEveryGapShape(t *testing.T) {
	shapes := []struct {
		name  string
		build func(t *testing.T, r *peerRig)
		gap   bool
	}{
		{"intact stream", func(t *testing.T, r *peerRig) {
			r.publishNodes(t, fleetKey, 1, 2)
			r.publishNodes(t, fleetKey, 3, 2)
		}, false},
		{"middle segment deleted", func(t *testing.T, r *peerRig) {
			r.publishNodes(t, fleetKey, 1, 2)
			r.publishNodes(t, fleetKey, 3, 2)
			r.publishNodes(t, fleetKey, 5, 2)
			require.NoError(t, os.Remove(r.segPath(2)))
		}, true},
		{"fresh reader finds segment 1 gone", func(t *testing.T, r *peerRig) {
			r.publishNodes(t, fleetKey, 1, 2)
			r.publishNodes(t, fleetKey, 3, 2)
			require.NoError(t, os.Remove(r.segPath(1)))
		}, true},
		{"rs jump across consecutive segment numbers after the first was read", func(t *testing.T, r *peerRig) {
			r.publishNodes(t, fleetKey, 1, 2)
			in, err := ingest.New(r.config())
			require.NoError(t, err)
			_, err = in.IngestAll(context.Background())
			require.NoError(t, err)
			r.rs += 3
			r.publishNodes(t, fleetKey, 6, 1)
		}, true},
		{"rs jump between later segments", func(t *testing.T, r *peerRig) {
			r.publishNodes(t, fleetKey, 1, 2)
			r.publishNodes(t, fleetKey, 3, 2)
			r.rs += 3
			r.publishNodes(t, fleetKey, 8, 1)
		}, true},
		{"hole below the clone position", func(t *testing.T, r *peerRig) {
			r.publishNodes(t, fleetKey, 1, 2)
			r.publishNodes(t, fleetKey, 3, 2)
			r.publishNodes(t, fleetKey, 5, 2)
			require.NoError(t, os.Remove(r.segPath(2)))
			r.seedClone(t, 6)
		}, false},
		{"clone position short of the earliest segment", func(t *testing.T, r *peerRig) {
			r.segNo, r.rs = 3, 11
			r.publishNodes(t, fleetKey, 11, 2)
			r.seedClone(t, 4)
		}, true},
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			r := newPeerRig(t)
			sh.build(t, r)
			doctor := r.doctorGaps(t)
			in, err := ingest.New(r.config())
			require.NoError(t, err)
			stats, err := in.IngestAll(context.Background())
			require.NoError(t, err)
			require.Equal(t, sh.gap, len(stats.Stalls) == 1, "ingest verdict")
			require.Equal(t, sh.gap, len(doctor) == 1, "doctor verdict must match ingest")
		})
	}
}
