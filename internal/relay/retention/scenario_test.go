// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The FR-21 §6.7 safety scenario, gathered in one file so the claim and
// its evidence sit together: a lagging peer blocks pruning, the
// retention floor holds regardless of acks, and a vanished peer cannot
// hold the window forever — but is warned about before anyone drops it
// from the quorum.
//
// The clone half of the scenario lives with the bootstrap package,
// which is where the code it exercises lives.

package retention_test

import (
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/retention"
	"github.com/stretchr/testify/require"
)

// TestPrune_LaggingPeerBlocksPruning is one of the scenario-25 tests: a
// peer that has not caught up holds the whole window open, however old
// the segments are. Pruning past it would drop history it can never
// recover except by re-bootstrapping.
func TestPrune_LaggingPeerBlocksPruning(t *testing.T) {
	in := input(seg(1, 10, 30), seg(2, 20, 30), seg(3, 30, 30))
	in.Acks = map[string]retention.Position{
		peerA: {SegmentNo: 3, RS: 30},
		peerB: {SegmentNo: 1, RS: 4}, // still inside segment 1
	}

	plan := retention.Plan(in)
	require.Empty(t, prunableNumbers(plan),
		"a single lagging peer holds every segment, no matter how old")
	for _, v := range plan {
		require.Contains(t, v.Reason, peerB, "the refusal names who is holding the window")
	}

	t.Run("and releases them as it catches up", func(t *testing.T) {
		in.Acks[peerB] = retention.Position{SegmentNo: 2, RS: 20}
		require.Equal(t, []uint64{1, 2}, prunableNumbers(retention.Plan(in)))
	})
}

// TestPrune_RetentionFloorHoldsEvenWhenAllAcked is the other
// scenario-25 half: a fleet that is fully caught up still cannot prune
// inside the retention window. The window is the recovery margin — it
// is what a peer restored from yesterday's backup reads to catch up, and
// acks say nothing about that.
func TestPrune_RetentionFloorHoldsEvenWhenAllAcked(t *testing.T) {
	in := input(seg(1, 10, 1), seg(2, 20, 3), seg(3, 30, 9))
	in.Acks = map[string]retention.Position{
		peerA: {SegmentNo: 3, RS: 30},
		peerB: {SegmentNo: 3, RS: 30},
	}

	plan := retention.Plan(in)
	require.Equal(t, []uint64{3}, prunableNumbers(plan),
		"only the segment past the retention floor may go, even with everyone acked")
	require.Contains(t, plan[0].Reason, "retention")
	require.Contains(t, plan[1].Reason, "retention")
}

// TestRetirePeer_LeavesTheQuorumAndDoctorWarnedFirst is a scenario-25
// test, and it pins an ORDER as much as an outcome.
//
// Retirement drops a peer from every prune quorum, which is exactly the
// power to delete history someone might need. So the silence that
// justifies it must be visible first: the detection flags the peer, an
// operator sees it and decides, and only then does the quorum shrink.
// The convention is "warned, then retired" — this asserts the warning
// half exists and fires before retirement is reasonable, without
// depending on how doctor eventually renders it.
func TestRetirePeer_LeavesTheQuorumAndDoctorWarnedFirst(t *testing.T) {
	relayDir := t.TempDir()
	keysDir := filepath.Join(t.TempDir(), "keys")
	_, err := lifecycle.Init(lifecycle.InitRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		RelayID: "01a0238b-d7d5-77cc-95c6-98a472ed7803", CreatedAt: now, CreatedBy: peerA,
		Projects:      []metadata.Project{{Prefix: "PROJ", FirstEventHash: "aaaa"}},
		Authenticated: true, Rand: rand.Reader,
	})
	require.NoError(t, err)

	// peerC has been silent well past the threshold; the others are live.
	silence := retention.SilenceInput{
		Peers:          []string{peerA, peerB, peerC},
		LastSeen:       map[string]time.Time{peerA: daysAgo(1), peerB: daysAgo(2), peerC: daysAgo(40)},
		SilentPeerDays: retention.DefaultSilentPeerDays,
		Now:            now,
	}

	// FIRST: the detection flags it, and flags only it.
	var flagged []string
	for _, s := range retention.SilentPeers(silence) {
		if s.Silent {
			flagged = append(flagged, s.PeerID)
		}
	}
	require.Equal(t, []string{peerC}, flagged,
		"the silence must be visible before anyone is dropped from the quorum")

	// And while it is merely flagged, it still holds the window: a
	// warning changes nothing on its own.
	in := input(seg(1, 10, 30))
	in.Peers = []string{peerA, peerB, peerC}
	in.Acks = map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 50}}
	require.Empty(t, prunableNumbers(retention.Plan(in)),
		"a flagged peer still blocks pruning until an operator acts")

	// THEN: the operator retires it, and only then does the quorum shrink.
	require.NoError(t, lifecycle.RetirePeer(relayDir, peerC))
	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)
	in.Retired = lifecycle.RetiredSet(doc)

	require.Equal(t, []uint64{1}, prunableNumbers(retention.Plan(in)),
		"retirement is what releases the window, not the warning")

	// And the retired peer stops being nagged about.
	silence.Retired = in.Retired
	for _, s := range retention.SilentPeers(silence) {
		require.NotEqual(t, peerC, s.PeerID, "a retired peer needs no further prompting")
	}
}
