// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package retention decides what an FR-21 relay may forget.
//
// The prune rule has two conditions and both are load-bearing (§6.7).
// Acks alone cannot shrink the safety window: the window is the
// recovery margin a peer restored from yesterday's backup reads to
// catch up, and every peer being caught up today says nothing about
// that. Age alone cannot outrun a slow peer: a laptop that has been
// shut for a week still needs the history it missed. Only both
// together release a segment, and losing either turns a routine prune
// into data another peer can never recover except by re-bootstrapping.
//
// Silent-peer detection lives here too, and it deliberately only
// PROMPTS retirement rather than performing it. A peer silent for a
// fortnight may be a laptop on holiday; dropping it from the quorum
// automatically would prune exactly the history it comes back for.
//
// The decisions are pure functions over facts a caller gathers, so
// they can be enumerated in tests rather than inferred from a live
// relay.
package retention

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultRetentionDays is sync.relay.retention_days' default (§6.7).
const DefaultRetentionDays = 7

// DefaultSilentPeerDays is sync.relay.silent_peer_days' default: twice
// the retention window, so a peer is only flagged once its silence has
// outlasted the margin that would have covered its return.
const DefaultSilentPeerDays = 2 * DefaultRetentionDays

// Position is one peer's acked position.
type Position struct {
	SegmentNo uint64 `json:"segment_no"`
	RS        uint64 `json:"rs"`
}

// Segment is what the pruner needs to know about one of this
// publisher's own sealed segments.
type Segment struct {
	// No is the segment number.
	No uint64

	// LastRS is the highest relay sequence the segment contains. A peer
	// has consumed the segment only once its ack reaches this.
	LastRS uint64

	// ModTime is the segment's age on the medium. It is the medium's
	// own timestamp rather than anything in the frame — the format
	// carries no wall clock, and a publisher's clock is not a fact a
	// reader's retention should depend on.
	ModTime time.Time
}

// PruneInput is the full set of facts the rule needs.
type PruneInput struct {
	// Segments are this publisher's sealed segments.
	Segments []Segment

	// ActiveNo is the segment being appended to. It is never prunable:
	// removing it would take a reader's resume point out from under it
	// mid-poll.
	ActiveNo uint64

	// Peers is every peer the relay knows of, retired or not.
	Peers []string

	// Acks is the publisher's view of who has read how far.
	Acks map[string]Position

	// Retired are peers dropped from the quorum by operator command.
	Retired map[string]bool

	// RetentionDays is the floor; zero takes the default.
	RetentionDays int

	// Now is the clock, injected.
	Now time.Time
}

// Verdict is one segment's disposition, with the reason when it stays.
type Verdict struct {
	Segment  Segment
	Prunable bool

	// Reason says why a segment is held, naming the peer or the floor
	// responsible so an operator can act on it rather than guess.
	Reason string
}

// Plan decides each sealed segment's fate, in the order given.
func Plan(in PruneInput) []Verdict {
	retentionDays := in.RetentionDays
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	floor := in.Now.AddDate(0, 0, -retentionDays)
	laggards := laggingPeers(in)

	out := make([]Verdict, 0, len(in.Segments))
	for _, s := range in.Segments {
		out = append(out, verdictFor(s, in, floor, laggards, retentionDays))
	}
	return out
}

// verdictFor applies the rule to one segment.
func verdictFor(s Segment, in PruneInput, floor time.Time, laggards map[uint64][]string, retentionDays int) Verdict {
	if s.No == in.ActiveNo {
		return Verdict{Segment: s, Reason: "the active segment is never pruned"}
	}
	if s.ModTime.After(floor) {
		return Verdict{Segment: s, Reason: fmt.Sprintf(
			"inside the %d-day retention window", retentionDays)}
	}
	if behind := laggards[s.No]; len(behind) > 0 {
		return Verdict{Segment: s, Reason: "not acked by " + strings.Join(behind, ", ")}
	}
	return Verdict{Segment: s, Prunable: true}
}

// laggingPeers maps each segment to the non-retired peers that have not
// acked past it. A peer with no ack at all counts as behind: it may be
// brand new or mid-bootstrap, and assuming it is caught up would prune
// the history it is about to read.
func laggingPeers(in PruneInput) map[uint64][]string {
	out := make(map[uint64][]string, len(in.Segments))
	for _, s := range in.Segments {
		var behind []string
		for _, peer := range in.Peers {
			if in.Retired[peer] {
				continue
			}
			if in.Acks[peer].RS < s.LastRS {
				behind = append(behind, peer)
			}
		}
		sort.Strings(behind)
		out[s.No] = behind
	}
	return out
}

// SilenceInput is what silent-peer detection needs.
type SilenceInput struct {
	// Peers is every peer the relay knows of.
	Peers []string

	// LastSeen is the most recent evidence of each peer, however the
	// caller measures it — an ack, a published segment. A peer absent
	// from the map has never been seen.
	LastSeen map[string]time.Time

	// Retired peers need no prompting and are omitted from the result.
	Retired map[string]bool

	// SilentPeerDays is the threshold; zero takes the default.
	SilentPeerDays int

	// Now is the clock, injected.
	Now time.Time
}

// Silence is one peer's finding. Every non-retired peer gets one, so a
// caller can render a full roster; Silent marks the ones that need
// attention.
type Silence struct {
	PeerID     string
	LastSeen   time.Time
	SilentDays int
	Silent     bool
}

// SilentPeers reports how long each peer has been quiet.
//
// It renders findings and nothing else. Retirement is an operator
// command precisely because this computation cannot tell a decommissioned
// machine from one whose owner is on leave, and only one of those should
// leave the prune quorum.
func SilentPeers(in SilenceInput) []Silence {
	threshold := in.SilentPeerDays
	if threshold <= 0 {
		threshold = DefaultSilentPeerDays
	}
	out := make([]Silence, 0, len(in.Peers))
	for _, peer := range in.Peers {
		if in.Retired[peer] {
			continue
		}
		seen, ok := in.LastSeen[peer]
		s := Silence{PeerID: peer}
		if !ok || seen.IsZero() {
			s.Silent = true
			out = append(out, s)
			continue
		}
		s.LastSeen = seen
		s.SilentDays = int(in.Now.Sub(seen).Hours() / 24)
		s.Silent = s.SilentDays >= threshold
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}
