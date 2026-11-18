// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// publishNodes publishes one segment holding one create_node per title.
func (r *peerRig) publishNodes(t *testing.T, key []byte, firstID int, count int) {
	t.Helper()
	var evs []model.SyncEvent
	for i := 0; i < count; i++ {
		id := firstID + i
		evs = append(evs, event(t, model.OpCreateNode, "PROJ-"+itoa(id), int64(id),
			&model.CreateNodePayload{Title: "n" + itoa(id)}))
	}
	r.publish(t, key, marshalEvents(t, evs...)...)
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func (r *peerRig) hasNode(t *testing.T, id string) bool {
	t.Helper()
	_, err := r.store.GetNode(context.Background(), id)
	return err == nil
}

func (r *peerRig) segPath(no uint64) string { return filepath.Join(r.segDir, segment.FileName(no)) }

func gapStall(t *testing.T, stats ingest.Stats) ingest.Stall {
	t.Helper()
	require.Len(t, stats.Stalls, 1, "a hole must stall the peer, not be stepped over")
	s := stats.Stalls[0]
	require.Equal(t, "RELAY_GAP", s.Code)
	require.Equal(t, remotePeer, s.PeerID)
	require.Contains(t, s.Reason, remotePeer, "the refusal names the peer")
	return s
}

// TestRelayIngest_MissingMiddleSegmentStallsWithRelayGap is MTIX-265: three
// segments, the middle one deleted. Before the fix the reader read segment 3 as
// if nothing were missing; the peer silently lacked segment 2's events.
func TestRelayIngest_MissingMiddleSegmentStallsWithRelayGap(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  []byte
	}{{"authenticated", fleetKey}, {"unauthenticated", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPeerRig(t)
			r.publishNodes(t, tc.key, 1, 2) // segment 1: rs 1-2
			r.publishNodes(t, tc.key, 3, 2) // segment 2: rs 3-4
			r.publishNodes(t, tc.key, 5, 2) // segment 3: rs 5-6
			require.NoError(t, os.Remove(r.segPath(2)))

			cfg := r.config()
			if tc.key == nil {
				cfg.Keys, cfg.Unauthenticated = nil, true
			}
			in, err := ingest.New(cfg)
			require.NoError(t, err)
			stats, err := in.IngestAll(context.Background())
			require.NoError(t, err)

			s := gapStall(t, stats)
			require.Contains(t, s.Reason, "missing relay sequence 3 through 4", "the refusal names the missing range")
			require.True(t, r.hasNode(t, "PROJ-1"))
			require.False(t, r.hasNode(t, "PROJ-5"), "nothing past the hole is applied")
			require.Equal(t, uint64(2), r.cursor(t).RS, "the cursor stays before the hole")

			again, err := in.IngestAll(context.Background())
			require.NoError(t, err)
			gapStall(t, again) // durable: every later poll meets the same hole
		})
	}
}

// TestRelayIngest_GapClearsWhenTheSegmentReturns proves the stall is not a
// tombstone: restoring the file lets the next poll apply everything in order.
func TestRelayIngest_GapClearsWhenTheSegmentReturns(t *testing.T) {
	r := newPeerRig(t)
	r.publishNodes(t, fleetKey, 1, 1)
	r.publishNodes(t, fleetKey, 2, 1)
	r.publishNodes(t, fleetKey, 3, 1)
	saved, err := os.ReadFile(r.segPath(2))
	require.NoError(t, err)
	require.NoError(t, os.Remove(r.segPath(2)))

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	gapStall(t, stats)

	require.NoError(t, os.WriteFile(r.segPath(2), saved, 0o600))
	stats, err = in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Empty(t, stats.Stalls)
	require.True(t, r.hasNode(t, "PROJ-2"))
	require.True(t, r.hasNode(t, "PROJ-3"))
	require.Equal(t, uint64(3), r.cursor(t).RS)
}

// TestRelayIngest_GapShapesAreAllRefused covers every way the segment sequence
// can skip: a fresh reader that finds no segment 1, a segment deleted after the
// reader had caught up, a number jump with contiguous rs, an rs jump with
// consecutive numbers, and a clone-seeded cursor.
func TestRelayIngest_GapShapesAreAllRefused(t *testing.T) {
	t.Run("fresh reader finds the first segments gone", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.publishNodes(t, fleetKey, 3, 2)
		require.NoError(t, os.Remove(r.segPath(1)))
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		gapStall(t, stats)
		require.Zero(t, r.journalCount(t))
	})

	t.Run("segment skipped after the reader caught up", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		_, err = in.IngestAll(context.Background())
		require.NoError(t, err)
		r.segNo++ // segment 2 never appears
		r.rs += 2
		r.publishNodes(t, fleetKey, 5, 1)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		gapStall(t, stats)
		require.False(t, r.hasNode(t, "PROJ-5"))
	})

	t.Run("relay sequence jumps across consecutive segment numbers", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.rs += 3 // records vanished from the numbering
		r.publishNodes(t, fleetKey, 6, 1)
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		s := gapStall(t, stats)
		require.Contains(t, s.Reason, "missing relay sequence 3 through 5")
		require.False(t, r.hasNode(t, "PROJ-6"))
	})

	t.Run("clone position falls short of the earliest segment", func(t *testing.T) {
		r := newPeerRig(t)
		r.segNo, r.rs = 3, 11
		r.publishNodes(t, fleetKey, 11, 2)
		require.NoError(t, r.store.AdvanceRelayIngestCursor(context.Background(), remotePeer,
			sqlite.RelayIngestPosition{RS: 4}))
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		s := gapStall(t, stats)
		require.Contains(t, s.Reason, "missing relay sequence 5 through 10")
	})

	t.Run("clone-seeded cursor with a hole between later segments", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 3) // seg 1: rs 1-3, below the clone position
		r.publishNodes(t, fleetKey, 4, 3) // seg 2: rs 4-6, holds the position
		r.publishNodes(t, fleetKey, 7, 3) // seg 3: rs 7-9, deleted
		r.publishNodes(t, fleetKey, 10, 3)
		require.NoError(t, os.Remove(r.segPath(3)))
		require.NoError(t, r.store.AdvanceRelayIngestCursor(context.Background(), remotePeer,
			sqlite.RelayIngestPosition{RS: 5}))
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		s := gapStall(t, stats)
		require.Contains(t, s.Reason, "missing relay sequence 7 through 9")
		require.False(t, r.hasNode(t, "PROJ-10"))
	})
}

// TestRelayIngest_ContiguousStreamsAreNotGaps guards against the check refusing
// healthy streams: a clone-seeded cursor that lines up, and a long run.
func TestRelayIngest_ContiguousStreamsAreNotGaps(t *testing.T) {
	t.Run("clone-seeded cursor inside the first segment", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 3)
		r.publishNodes(t, fleetKey, 4, 3)
		require.NoError(t, r.store.AdvanceRelayIngestCursor(context.Background(), remotePeer,
			sqlite.RelayIngestPosition{RS: 2}))
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		require.Empty(t, stats.Stalls)
		require.Equal(t, uint64(6), r.cursor(t).RS)
	})

	t.Run("six consecutive segments", func(t *testing.T) {
		r := newPeerRig(t)
		for i := 1; i <= 6; i++ {
			r.publishNodes(t, fleetKey, i, 1)
		}
		in, err := ingest.New(r.config())
		require.NoError(t, err)
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		require.Empty(t, stats.Stalls)
		require.Equal(t, 6, stats.Applied)
	})
}

// TestFindGaps_ReportsHolesWithoutApplyingAnything is the doctor's read-only view.
func TestFindGaps_ReportsHolesWithoutApplyingAnything(t *testing.T) {
	cursor := func(r *peerRig) func(string) (ingest.Position, error) {
		return func(peer string) (ingest.Position, error) {
			p, err := r.store.RelayIngestCursor(context.Background(), peer)
			return ingest.Position{SegmentNo: p.SegmentNo, RS: p.RS, PubEpoch: p.PubEpoch}, err
		}
	}

	t.Run("middle segment missing", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.publishNodes(t, fleetKey, 3, 2)
		r.publishNodes(t, fleetKey, 5, 2)
		require.NoError(t, os.Remove(r.segPath(2)))
		rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer, cursor(r), r.config().Keys)
		require.NoError(t, err)
		gaps := rep.Gaps
		require.Len(t, gaps, 1)
		require.Equal(t, remotePeer, gaps[0].PeerID)
		require.Contains(t, gaps[0].Reason, "missing relay sequence 3 through 4")
		require.Zero(t, r.journalCount(t), "doctor applies nothing")
	})

	t.Run("intact stream and own directory", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.publishNodes(t, fleetKey, 3, 2)
		rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer, cursor(r), r.config().Keys)
		require.NoError(t, err)
		require.Empty(t, rep.Gaps)
		require.Empty(t, rep.Unchecked)
	})

	t.Run("cursor behind the earliest segment", func(t *testing.T) {
		r := newPeerRig(t)
		r.publishNodes(t, fleetKey, 1, 2)
		r.publishNodes(t, fleetKey, 3, 2)
		require.NoError(t, os.Remove(r.segPath(1)))
		rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer, cursor(r), r.config().Keys)
		require.NoError(t, err)
		require.Len(t, rep.Gaps, 1)
	})

	t.Run("missing peers directory is an error, not a pass", func(t *testing.T) {
		_, err := ingest.FindGaps(context.Background(), filepath.Join(t.TempDir(), "nope"), selfPeer, cursor(newPeerRig(t)), nil)
		require.Error(t, err)
	})
}
