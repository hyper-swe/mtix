// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const (
	peerD = "1111222233334444"
	peerC = "aaaabbbbccccdddd"
)

// joinPeer opens a new peer on the gap rig's relay with its own store.
func (g *gapRig) joinPeer(t *testing.T, id string) (*tick.Stack, *sqlite.Store) {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), id+".db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ring, err := keyring.Load(g.a.keysDir)
	require.NoError(t, err)
	epoch, key, err := ring.Current()
	require.NoError(t, err)
	keys := filepath.Join(t.TempDir(), "keys-"+id)
	require.NoError(t, keyring.Write(keys, epoch, key))
	stack, err := tick.Open(tick.StackConfig{
		Store: st, RelayDir: g.a.relayDir, KeysDir: keys, PeerID: id, Logger: slog.Default(),
		MaxSegmentBytes: 1, // every pass seals its segment, as the publisher's does
	})
	require.NoError(t, err)
	return stack, st
}

// exportSnapshot is what `mtix sync relay clone --export` does in the exporting
// project: stamp what that project holds of every peer stream, through the same
// StampPositions the verb uses.
func (g *gapRig) exportSnapshot(t *testing.T, st *sqlite.Store, by string) {
	t.Helper()
	ctx := context.Background()
	peers, _, err := segment.ListPeers(filepath.Join(g.a.relayDir, tick.PeersDirName))
	require.NoError(t, err)
	ids := make([]string, 0, len(peers))
	for _, p := range peers {
		ids = append(ids, p.PeerID)
	}
	names, err := bootstrap.SnapshotNames(g.a.relayDir)
	require.NoError(t, err)
	stamp, err := bootstrap.StampPositions(ctx, st, by, ids)
	require.NoError(t, err)
	_, err = bootstrap.ExportSnapshot(ctx, bootstrap.ExportRequest{
		Store: st, RelayDir: g.a.relayDir, Project: "PROJ", ExportedBy: by,
		CreatedAt: fixedTime.Add(time.Duration(len(names)+1) * time.Hour),
		Positions: stamp.Positions, Epochs: stamp.Epochs,
	})
	require.NoError(t, err)
}

// stalledPeers lists the peers a pass stalled on.
func stalledPeers(stalls []ingest.Stall) []string {
	out := make([]string, 0, len(stalls))
	for _, s := range stalls {
		out = append(out, s.PeerID)
	}
	return out
}

// pruneSegment removes one sealed segment of a peer, as retention does.
func (g *gapRig) pruneSegment(t *testing.T, peer string, no uint64) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(g.a.relayDir, tick.PeersDirName, peer,
		tick.SegmentsDirName, segment.FileName(no))))
}

// publishNodes creates nodes in a project and publishes one segment per node.
func publishNodes(t *testing.T, stack *tick.Stack, st *sqlite.Store, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for i, id := range ids {
		require.NoError(t, st.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Seq: 100 + i, Title: id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeStory, Creator: "t",
			ContentHash: model.ComputeContentHash(id, "", "", "", nil),
			CreatedAt:   fixedTime, UpdatedAt: fixedTime,
		}))
		_, err := stack.Pass(ctx)
		require.NoError(t, err)
	}
}

// cloneFresh is `mtix sync relay clone` in a brand-new project, then one pass.
func (g *gapRig) cloneFresh(t *testing.T) (*tick.Stack, tick.Result) {
	t.Helper()
	ctx := context.Background()
	stack, st := g.joinPeer(t, peerC)
	names, err := bootstrap.SnapshotNames(g.a.relayDir)
	require.NoError(t, err)
	res, err := bootstrap.ImportSnapshot(ctx, bootstrap.ImportRequest{
		Store: st, Path: filepath.Join(g.a.relayDir, bootstrap.DirName, names[len(names)-1]),
		Options: sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge},
	})
	require.NoError(t, err)
	require.NoError(t, bootstrap.SeedCloneCursors(ctx, st, res), "the production seeding path")
	pass, err := stack.Pass(ctx)
	require.NoError(t, err)
	g.cloned = st
	return stack, pass
}

// TestRelayGap_RejoinNeedsASnapshotFromAPeerPastTheHole works out by test which
// snapshot source recovers a RELAY_GAP (MTIX-265). Segment 2 of peer A is lost.
// A snapshot exported by the stalled project stalls the new project at the same
// hole. One exported by the publisher itself recovers, since the publisher holds
// every event it published, and so does one exported by a project that had
// already ingested past the hole. The exporting peer D has itself published and
// had its own oldest segment pruned, so its own stream must not stall either.
func TestRelayGap_RejoinNeedsASnapshotFromAPeerPastTheHole(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	// Peer D reads all three segments before the loss.
	stackD, dStore := g.joinPeer(t, peerD)
	_, err := stackD.Pass(ctx)
	require.NoError(t, err)
	// D then publishes two segments of its own, and retention prunes its first.
	publishNodes(t, stackD, dStore, "PROJ-8", "PROJ-9")
	_, err = g.stackA.Pass(ctx) // the publisher reads D's segments before they go
	require.NoError(t, err)
	g.pruneSegment(t, peerD, 1)
	require.NoError(t, os.Remove(g.segPath(2)))

	// B has read nothing: it stalls at A's hole and, as a fresh reader, at D's
	// pruned start.
	res, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{peerA, peerD}, stalledPeers(res.Ingest.Stalls))

	t.Run("snapshot from the stalled project stalls again", func(t *testing.T) {
		g.exportSnapshot(t, g.bStore, peerB)
		_, pass := g.cloneFresh(t)
		require.ElementsMatch(t, []string{peerA, peerD}, stalledPeers(pass.Ingest.Stalls))
		require.Equal(t, "RELAY_GAP", pass.Ingest.Stalls[0].Code)
	})
	t.Run("snapshot from the publisher recovers, it holds its own events", func(t *testing.T) {
		g.exportSnapshot(t, g.a.store, peerA)
		_, pass := g.cloneFresh(t)
		require.Empty(t, pass.Ingest.Stalls)
		require.True(t, g.hasIn(t, g.cloned, "PROJ-2"))
	})
	t.Run("snapshot from a project that read past the hole recovers", func(t *testing.T) {
		g.exportSnapshot(t, dStore, peerD)
		stackC, pass := g.cloneFresh(t)
		require.Empty(t, pass.Ingest.Stalls)
		require.True(t, g.hasIn(t, g.cloned, "PROJ-3"), "the snapshot carries the events the hole lost")
		require.True(t, g.hasIn(t, g.cloned, "PROJ-8"), "and the exporter's own events")

		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
		next, err := stackC.Pass(ctx)
		require.NoError(t, err)
		require.Empty(t, next.Ingest.Stalls)
		require.True(t, g.hasIn(t, g.cloned, "PROJ-4"), "the new project keeps tailing past the hole")
	})
}

func (g *gapRig) hasIn(t *testing.T, st *sqlite.Store, id string) bool {
	t.Helper()
	_, err := st.GetNode(context.Background(), id)
	return err == nil
}

// TestRelayGap_CloneFromAnExporterWhoseOwnSegmentWasPrunedDoesNotStall is the
// healthy-relay case (MTIX-265 review): retention pruned the oldest sealed
// segment of the exporting peer's own stream. The snapshot holds those events,
// so a clone must tail past them, not stall RELAY_GAP on a stream it never
// needed segment 1 of.
func TestRelayGap_CloneFromAnExporterWhoseOwnSegmentWasPrunedDoesNotStall(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	_, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	g.pruneSegment(t, peerA, 1)

	g.exportSnapshot(t, g.a.store, peerA)
	stackC, pass := g.cloneFresh(t)
	require.Empty(t, pass.Ingest.Stalls, "a healthy pruned relay clones without a stall")
	require.True(t, g.hasIn(t, g.cloned, "PROJ-1"), "the snapshot carries the pruned segment's events")

	g.a.journal(t, 1)
	_, err = g.stackA.Pass(ctx)
	require.NoError(t, err)
	next, err := stackC.Pass(ctx)
	require.NoError(t, err)
	require.Empty(t, next.Ingest.Stalls)
	require.True(t, g.hasIn(t, g.cloned, "PROJ-4"), "the clone keeps tailing the exporter")
}

// TestRelayGap_CloneAfterResetPeerBeforeTheFirstPublishDoesNotStall: reset-peer
// with base rs 1 leaves the exporter's next relay sequence at 1 in a new epoch
// while the old epoch's segments are still on the medium. The snapshot holds
// them, so the clone must not read the pruned first segment as a hole.
func TestRelayGap_CloneAfterResetPeerBeforeTheFirstPublishDoesNotStall(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, g.stackA.Publisher.ResetPeer(ctx, 0, 1))
	g.pruneSegment(t, peerA, 1)

	g.exportSnapshot(t, g.a.store, peerA)
	_, pass := g.cloneFresh(t)
	require.Empty(t, pass.Ingest.Stalls)
	require.True(t, g.hasIn(t, g.cloned, "PROJ-1"))
}

// TestRelayGap_LegitimateResetPeerPassesTheEpochChangeRule: a reset-peer that
// lost no segment is not a hole, however the reader met the stream.
func TestRelayGap_LegitimateResetPeerPassesTheEpochChangeRule(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	_, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.NoError(t, g.stackA.Publisher.ResetPeer(ctx, 0, 1))
	g.a.journal(t, 1)
	_, err = g.stackA.Pass(ctx)
	require.NoError(t, err)

	again, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Empty(t, again.Ingest.Stalls, "a reader mid-stream follows the reset")
	require.True(t, g.has(t, "PROJ-4"))

	// A reader that meets the stream after the reset reads the same segments.
	_, pass := g.joinThenPass(t)
	require.Empty(t, pass.Ingest.Stalls, "a fresh reader reads across the reset")
}

// joinThenPass is a brand-new project reading the relay from nothing.
func (g *gapRig) joinThenPass(t *testing.T) (*tick.Stack, tick.Result) {
	t.Helper()
	stack, _ := g.joinPeer(t, peerD)
	pass, err := stack.Pass(context.Background())
	require.NoError(t, err)
	return stack, pass
}
