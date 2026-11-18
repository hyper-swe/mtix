// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRelayIngest_CloneOfACloneThatExportedBeforeItsFirstPollDoesNotStall: a
// clone seeded in epoch 2 that exports before it has polled must stamp epoch 2,
// not the epoch-0 cursor, or a clone of that export reads the healthy
// old-epoch segment (rs 401) as a hole and stalls RELAY_GAP (MTIX-265). The
// export uses the production StampPositions and the import the production seed.
func TestRelayIngest_CloneOfACloneThatExportedBeforeItsFirstPollDoesNotStall(t *testing.T) {
	ctx := context.Background()
	r := newPeerRig(t)
	r.segNo, r.rs = 5, 401
	r.publishNodes(t, fleetKey, 401, 2) // epoch 1, rs 401-402, the oldest retained
	r.pubEpoch, r.rs = 2, 1
	r.publishNodes(t, fleetKey, 1, 2) // epoch 2, rs 1-2
	r.publishNodes(t, fleetKey, 3, 2) // epoch 2, rs 3-4
	r.seedCloneEpoch(t, 2, 2)         // clone C1, before its first poll

	stamp, err := bootstrap.StampPositions(ctx, r.store, selfPeer, []string{remotePeer})
	require.NoError(t, err)
	require.Equal(t, uint64(2), stamp.Positions[remotePeer])
	require.Equal(t, uint16(2), stamp.Epochs[remotePeer], "the export carries the stamped epoch")

	c2, err := sqlite.New(filepath.Join(t.TempDir(), "c2.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c2.Close() })
	require.NoError(t, bootstrap.SeedCloneCursors(ctx, c2, bootstrap.ImportResult{
		Positions: stamp.Positions, Epochs: stamp.Epochs,
	}))

	cfg := r.config()
	cfg.Store = c2
	rep, err := ingest.FindGaps(ctx, r.peersDir, selfPeer, func(peer string) (ingest.Position, error) {
		p, curErr := c2.RelayIngestCursor(ctx, peer)
		if curErr != nil {
			return ingest.Position{}, curErr
		}
		e, stErr := c2.RelayCloneStampEpoch(ctx, peer)
		return ingest.StampedPosition(ingest.Position{SegmentNo: p.SegmentNo, RS: p.RS, PubEpoch: p.PubEpoch}, e), stErr
	}, cfg.Keys)
	require.NoError(t, err)
	require.Empty(t, rep.Gaps, "doctor agrees")

	in, err := ingest.New(cfg)
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Empty(t, stats.Stalls, "the old-epoch segment lies inside the snapshot")
}
