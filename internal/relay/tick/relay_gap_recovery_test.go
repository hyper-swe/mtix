// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// gapRig is peer A publishing one segment per pass and peer B reading it.
type gapRig struct {
	a      *relayRig
	stackA *tick.Stack
	stackB *tick.Stack
	bStore *sqlite.Store
	cloned *sqlite.Store // the store of the project a test clones into
}

func newGapRig(t *testing.T) *gapRig {
	t.Helper()
	a := newRelay(t, true)
	cfg := a.config()
	cfg.MaxSegmentBytes = 1 // every pass seals its segment
	stackA, err := tick.Open(cfg)
	require.NoError(t, err)

	bStore, err := sqlite.New(filepath.Join(t.TempDir(), "b.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = bStore.Close() })
	ring, err := keyring.Load(a.keysDir)
	require.NoError(t, err)
	epoch, key, err := ring.Current()
	require.NoError(t, err)
	keysB := filepath.Join(t.TempDir(), "keys-b")
	require.NoError(t, keyring.Write(keysB, epoch, key))
	stackB, err := tick.Open(tick.StackConfig{
		Store: bStore, RelayDir: a.relayDir, KeysDir: keysB, PeerID: peerB, Logger: slog.Default(),
	})
	require.NoError(t, err)
	return &gapRig{a: a, stackA: stackA, stackB: stackB, bStore: bStore}
}

func (g *gapRig) segPath(no uint64) string {
	return filepath.Join(g.a.relayDir, tick.PeersDirName, peerA, tick.SegmentsDirName, segment.FileName(no))
}

func (g *gapRig) has(t *testing.T, id string) bool {
	t.Helper()
	_, err := g.bStore.GetNode(context.Background(), id)
	return err == nil
}

// TestRelayGap_RepublishFromHeldRSDoesNotHealAHoleInTheMiddle pins the finding
// behind the RELAY_GAP fix text (MTIX-265): republish re-emits into FRESH
// segments AFTER the existing tail, so a reader stalled on a hole in the middle
// never reaches them. The recovery is restoring the file or re-bootstrapping.
func TestRelayGap_RepublishFromHeldRSDoesNotHealAHoleInTheMiddle(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, os.Remove(g.segPath(2)))

	res, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, res.Ingest.Stalls, 1)
	require.Equal(t, "RELAY_GAP", res.Ingest.Stalls[0].Code)
	require.True(t, g.has(t, "PROJ-1"))
	require.False(t, g.has(t, "PROJ-3"))

	// The old guidance: the peer republishes from the rs the reader holds.
	n, err := g.stackA.Publisher.RepublishFrom(ctx, 1)
	require.NoError(t, err)
	require.Positive(t, n)

	again, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, again.Ingest.Stalls, 1, "republish does not let the reader past the hole")
	require.Equal(t, "RELAY_GAP", again.Ingest.Stalls[0].Code)
	require.False(t, g.has(t, "PROJ-3"), "the republished events never arrive")
}

// TestRelayGap_ResetPeerDoesNotHealAHoleInTheMiddle: reset-peer bumps the
// publisher epoch and republishes into fresh segments, but they follow the old
// tail, and the reader stops at the hole before reaching them.
func TestRelayGap_ResetPeerDoesNotHealAHoleInTheMiddle(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, os.Remove(g.segPath(2)))
	res, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, res.Ingest.Stalls, 1)

	require.NoError(t, g.stackA.Publisher.ResetPeer(ctx, 0, 1))
	_, err = g.stackA.Publisher.PublishPending(ctx)
	require.NoError(t, err)

	again, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, again.Ingest.Stalls, 1)
	require.Equal(t, "RELAY_GAP", again.Ingest.Stalls[0].Code)
	require.False(t, g.has(t, "PROJ-3"))
}

// TestRelayGap_RestoringTheSegmentHeals is the recovery the fix text leads with.
func TestRelayGap_RestoringTheSegmentHeals(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	saved, err := os.ReadFile(g.segPath(2))
	require.NoError(t, err)
	require.NoError(t, os.Remove(g.segPath(2)))
	res, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, res.Ingest.Stalls, 1)

	require.NoError(t, os.WriteFile(g.segPath(2), saved, 0o600))
	again, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Empty(t, again.Ingest.Stalls)
	require.True(t, g.has(t, "PROJ-3"))
}

// TestRelayGap_RepublishDoesNotHealACorruptMiddleSegment is the same finding
// for the corrupt-segment guidance, which names the same republish command.
func TestRelayGap_RepublishDoesNotHealACorruptMiddleSegment(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 3; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	raw, err := os.ReadFile(g.segPath(2))
	require.NoError(t, err)
	copy(raw[0:4], "XXXX")
	require.NoError(t, os.WriteFile(g.segPath(2), raw, 0o600))

	res, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, res.Ingest.Stalls, 1)
	require.Equal(t, "RELAY_SEGMENT_CORRUPT", res.Ingest.Stalls[0].Code)

	_, err = g.stackA.Publisher.RepublishFrom(ctx, 1)
	require.NoError(t, err)
	again, err := g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Len(t, again.Ingest.Stalls, 1, "republish does not clear a corrupt middle segment either")
	require.False(t, g.has(t, "PROJ-3"))
}
