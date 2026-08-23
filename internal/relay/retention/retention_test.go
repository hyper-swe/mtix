// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package retention_test

import (
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/retention"
	"github.com/stretchr/testify/require"
)

const (
	peerA = "0123456789abcdef"
	peerB = "fedcba9876543210"
	peerC = "aaaaaaaaaaaaaaaa"
)

var now = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return now.AddDate(0, 0, -d) }

func seg(no, lastRS uint64, ageDays int) retention.Segment {
	return retention.Segment{No: no, LastRS: lastRS, ModTime: daysAgo(ageDays)}
}

func input(segs ...retention.Segment) retention.PruneInput {
	return retention.PruneInput{
		Segments:      segs,
		ActiveNo:      99,
		Peers:         []string{peerA, peerB},
		RetentionDays: retention.DefaultRetentionDays,
		Now:           now,
	}
}

// prunableNumbers extracts the segment numbers a plan would remove.
func prunableNumbers(vs []retention.Verdict) []uint64 {
	var out []uint64
	for _, v := range vs {
		if v.Prunable {
			out = append(out, v.Segment.No)
		}
	}
	return out
}

// TestPrune_BothConditionsRequired is the FR-21 §6.7 rule stated as a
// truth table. Acks alone cannot shrink the safety window; age alone
// cannot outrun a slow peer. Only both together release a segment.
func TestPrune_BothConditionsRequired(t *testing.T) {
	tests := []struct {
		name     string
		ageDays  int
		acks     map[string]retention.Position
		prunable bool
	}{
		{"everyone acked and old enough", 10,
			map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 50}}, true},
		{"everyone acked but too young", 1,
			map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 50}}, false},
		{"old enough but a peer is behind", 10,
			map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 5}}, false},
		{"neither", 1,
			map[string]retention.Position{peerA: {RS: 5}, peerB: {RS: 5}}, false},
		{"exactly at the retention boundary", retention.DefaultRetentionDays,
			map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 50}}, true},
		{"acked exactly to the segment's last record", 10,
			map[string]retention.Position{peerA: {RS: 10}, peerB: {RS: 10}}, true},
		{"one short of the segment's last record", 10,
			map[string]retention.Position{peerA: {RS: 10}, peerB: {RS: 9}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input(seg(1, 10, tt.ageDays))
			in.Acks = tt.acks

			plan := retention.Plan(in)
			require.Len(t, plan, 1)
			require.Equal(t, tt.prunable, plan[0].Prunable, "reason: %s", plan[0].Reason)
			if !tt.prunable {
				require.NotEmpty(t, plan[0].Reason, "a refusal must say why")
			}
		})
	}
}

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

// TestPrune_ActiveSegmentIsNeverPrunable keeps the tail out of reach.
// Removing the segment a publisher is appending to would take a reader's
// resume point out from under it mid-poll.
func TestPrune_ActiveSegmentIsNeverPrunable(t *testing.T) {
	in := input(seg(1, 10, 30), seg(7, 70, 30))
	in.ActiveNo = 7
	in.Acks = map[string]retention.Position{
		peerA: {SegmentNo: 7, RS: 70}, peerB: {SegmentNo: 7, RS: 70},
	}

	plan := retention.Plan(in)
	require.Equal(t, []uint64{1}, prunableNumbers(plan))
	require.Contains(t, plan[1].Reason, "active")
}

// TestPrune_APeerWithNoAckBlocks treats an unknown position as behind.
// A peer that has never written an ack file may be brand new or may be
// mid-bootstrap; assuming it is caught up would prune the history it is
// about to read.
func TestPrune_APeerWithNoAckBlocks(t *testing.T) {
	in := input(seg(1, 10, 30))
	in.Peers = []string{peerA, peerB, peerC}
	in.Acks = map[string]retention.Position{
		peerA: {RS: 50}, peerB: {RS: 50},
	}

	plan := retention.Plan(in)
	require.Empty(t, prunableNumbers(plan))
	require.Contains(t, plan[0].Reason, peerC)
}

// TestPrune_RetiredPeersLeaveTheQuorum is the ghost-peer cure. Without
// it a peer that vanished forever never acks and stalls pruning
// permanently — the relay grows without bound because of a machine
// nobody has seen in months.
func TestPrune_RetiredPeersLeaveTheQuorum(t *testing.T) {
	in := input(seg(1, 10, 30))
	in.Peers = []string{peerA, peerB, peerC}
	in.Acks = map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 50}}

	require.Empty(t, prunableNumbers(retention.Plan(in)), "the ghost holds the window")

	in.Retired = map[string]bool{peerC: true}
	require.Equal(t, []uint64{1}, prunableNumbers(retention.Plan(in)),
		"retiring the ghost releases it")

	t.Run("a retired peer's stale ack is ignored too", func(t *testing.T) {
		in.Acks[peerC] = retention.Position{RS: 1}
		require.Equal(t, []uint64{1}, prunableNumbers(retention.Plan(in)))
	})
}

// TestPrune_NoPeersAtAll covers a relay with one member: there is no
// quorum to satisfy, so only retention holds.
func TestPrune_NoPeersAtAll(t *testing.T) {
	in := input(seg(1, 10, 30))
	in.Peers = nil
	require.Equal(t, []uint64{1}, prunableNumbers(retention.Plan(in)))
}

// TestPrune_EveryPeerRetired is the degenerate case of the same thing.
func TestPrune_EveryPeerRetired(t *testing.T) {
	in := input(seg(1, 10, 30))
	in.Retired = map[string]bool{peerA: true, peerB: true}
	require.Equal(t, []uint64{1}, prunableNumbers(retention.Plan(in)))
}

// TestPrune_DefaultsAreApplied keeps a zero-valued config from meaning
// "prune everything immediately".
func TestPrune_DefaultsAreApplied(t *testing.T) {
	in := input(seg(1, 10, 3))
	in.RetentionDays = 0
	in.Acks = map[string]retention.Position{peerA: {RS: 50}, peerB: {RS: 50}}

	plan := retention.Plan(in)
	require.False(t, plan[0].Prunable,
		"an unset retention must fall back to the default, never to zero")
}

// TestSilentPeers is the detection that PROMPTS retirement rather than
// performing it. Retirement is operator-only by design: a peer silent
// for a fortnight may be a laptop on holiday, and automatically dropping
// it from the quorum would prune history it comes back for.
func TestSilentPeers(t *testing.T) {
	in := retention.SilenceInput{
		LastSeen: map[string]time.Time{
			peerA: daysAgo(1),
			peerB: daysAgo(20),
			peerC: daysAgo(15),
		},
		Peers:          []string{peerA, peerB, peerC},
		SilentPeerDays: retention.DefaultSilentPeerDays,
		Now:            now,
	}

	found := retention.SilentPeers(in)
	byPeer := map[string]retention.Silence{}
	for _, s := range found {
		byPeer[s.PeerID] = s
	}
	require.Len(t, found, 3, "every peer is reported; Silent says which need attention")
	require.False(t, byPeer[peerA].Silent)
	require.True(t, byPeer[peerB].Silent)
	require.True(t, byPeer[peerC].Silent)
	require.Equal(t, 20, byPeer[peerB].SilentDays)

	t.Run("the default is twice the retention window", func(t *testing.T) {
		require.Equal(t, 2*retention.DefaultRetentionDays, retention.DefaultSilentPeerDays)
	})

	t.Run("an unset threshold falls back to the default", func(t *testing.T) {
		in.SilentPeerDays = 0
		found := retention.SilentPeers(in)
		for _, s := range found {
			if s.PeerID == peerA {
				require.False(t, s.Silent, "a peer seen yesterday is never silent")
			}
		}
	})

	t.Run("a peer never seen at all is silent", func(t *testing.T) {
		in.Peers = append(in.Peers, "bbbbbbbbbbbbbbbb")
		in.SilentPeerDays = retention.DefaultSilentPeerDays
		for _, s := range retention.SilentPeers(in) {
			if s.PeerID == "bbbbbbbbbbbbbbbb" {
				require.True(t, s.Silent)
				require.True(t, s.LastSeen.IsZero())
			}
		}
	})

	t.Run("retired peers are not reported as needing retirement", func(t *testing.T) {
		in.Retired = map[string]bool{peerB: true}
		for _, s := range retention.SilentPeers(in) {
			require.NotEqual(t, peerB, s.PeerID, "a retired peer needs no prompting")
		}
	})
}
