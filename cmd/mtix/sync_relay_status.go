// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/retention"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/spf13/cobra"
)

// relayPeerStatus is one peer's line in `relay status`.
type relayPeerStatus struct {
	PeerID    string `json:"peer_id"`
	SegmentNo uint64 `json:"ingested_segment"`
	RS        uint64 `json:"ingested_rs"`
	PubEpoch  uint16 `json:"pub_epoch"`
	Retired   bool   `json:"retired"`
	Silent    bool   `json:"silent"`
	Self      bool   `json:"self"`
}

// relayStatusReport is what `relay status` shows, and what --json emits.
type relayStatusReport struct {
	Dir            string            `json:"dir"`
	RelayID        string            `json:"relay_id"`
	Authenticated  bool              `json:"authenticated"`
	KeyEpoch       uint16            `json:"key_epoch"`
	Self           string            `json:"self"`
	PublishedRS    uint64            `json:"published_rs"`
	PublisherEpoch uint16            `json:"publisher_epoch"`
	Peers          []relayPeerStatus `json:"peers"`
	Snapshots      []string          `json:"snapshots"`
	ForeignEntries []string          `json:"foreign_entries"`
}

// relayPeerDirs lists the peer ids under a relay, plus entries that do
// not match the identity grammar.
func relayPeerDirs(dir string) ([]string, []string, error) {
	peers, foreign, err := segment.ListPeers(filepath.Join(dir, tick.PeersDirName))
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(peers))
	for _, p := range peers {
		ids = append(ids, p.PeerID)
	}
	return ids, foreign, nil
}

// collectRelayStatus gathers what an operator needs to see.
func collectRelayStatus(ctx context.Context) (relayStatusReport, error) {
	dir, err := requireRelay()
	if err != nil {
		return relayStatusReport{}, err
	}
	doc, err := metadata.Read(dir)
	if err != nil {
		return relayStatusReport{}, err
	}
	self, err := relayPeerID()
	if err != nil {
		return relayStatusReport{}, err
	}
	rep := relayStatusReport{
		Dir: dir, RelayID: doc.RelayID, Authenticated: doc.Authenticated, Self: self,
	}
	if epoch, ok := doc.CurrentKeyEpoch(); ok {
		rep.KeyEpoch = epoch
	}
	if pos, posErr := app.store.RelayPushCursor(ctx); posErr == nil {
		rep.PublishedRS = pos.NextRS - 1
		rep.PublisherEpoch = pos.PubEpoch
	}

	ids, foreign, err := relayPeerDirs(dir)
	if err != nil {
		return relayStatusReport{}, err
	}
	rep.ForeignEntries = foreign

	retired := map[string]bool{}
	for _, p := range doc.RetiredPeers {
		retired[p] = true
	}
	silent := map[string]bool{}
	for _, s := range retention.SilentPeers(retention.SilenceInput{
		Peers:          ids,
		LastSeen:       relayLastSeen(dir, ids),
		Retired:        retired,
		SilentPeerDays: relayConfigInt("sync.relay.silent_peer_days", retention.DefaultSilentPeerDays),
		Now:            time.Now().UTC(),
	}) {
		silent[s.PeerID] = s.Silent
	}

	for _, id := range ids {
		line := relayPeerStatus{PeerID: id, Retired: retired[id], Silent: silent[id], Self: id == self}
		if pos, err := app.store.RelayIngestCursor(ctx, id); err == nil {
			line.SegmentNo, line.RS, line.PubEpoch = pos.SegmentNo, pos.RS, pos.PubEpoch
		}
		rep.Peers = append(rep.Peers, line)
	}
	sort.Slice(rep.Peers, func(i, j int) bool { return rep.Peers[i].PeerID < rep.Peers[j].PeerID })

	if names, err := bootstrap.SnapshotNames(dir); err == nil {
		rep.Snapshots = names
	}
	return rep, nil
}

// relayLastSeen reads each peer's newest segment mtime as the evidence
// of when it was last active. The medium's own timestamp is used rather
// than anything in a frame: the format carries no wall clock, and a
// remote publisher's clock is not a fact this peer's retention should
// depend on.
func relayLastSeen(dir string, peers []string) map[string]time.Time {
	out := make(map[string]time.Time, len(peers))
	for _, peer := range peers {
		segDir := filepath.Join(dir, tick.PeersDirName, peer, tick.SegmentsDirName)
		segs, _, err := segment.ListSegments(segDir)
		if err != nil || len(segs) == 0 {
			continue
		}
		newest := time.Time{}
		for _, s := range segs {
			if info, err := relayStat(s.Path); err == nil && info.After(newest) {
				newest = info
			}
		}
		if !newest.IsZero() {
			out[peer] = newest
		}
	}
	return out
}

// printRelayStatus renders the report for a human.
func printRelayStatus(cmd *cobra.Command, rep relayStatusReport) {
	out := cmd.OutOrStdout()
	mode := "authenticated"
	if !rep.Authenticated {
		mode = "UNAUTHENTICATED"
	}
	fmt.Fprintf(out, "relay %s at %s (%s", rep.RelayID, rep.Dir, mode)
	if rep.Authenticated {
		fmt.Fprintf(out, ", key epoch %d", rep.KeyEpoch)
	}
	fmt.Fprintf(out, ")\n")
	fmt.Fprintf(out, "this peer %s: published through rs %d, publisher epoch %d\n",
		rep.Self, rep.PublishedRS, rep.PublisherEpoch)

	fmt.Fprintf(out, "peers:\n")
	for _, p := range rep.Peers {
		marks := ""
		switch {
		case p.Self:
			marks = " (this peer)"
		case p.Retired:
			marks = " RETIRED"
		case p.Silent:
			marks = " SILENT — consider `mtix sync relay retire-peer " + p.PeerID + "`"
		}
		fmt.Fprintf(out, "  %s  ingested segment %d rs %d (epoch %d)%s\n",
			p.PeerID, p.SegmentNo, p.RS, p.PubEpoch, marks)
	}
	for _, name := range rep.Snapshots {
		fmt.Fprintf(out, "bootstrap snapshot present: %s\n", name)
	}
	for _, name := range rep.ForeignEntries {
		fmt.Fprintf(out, "foreign entry ignored: %s\n", name)
	}
}

// relayStat returns a path's modification time, without following a
// link — the relay is a medium other software can write to.
func relayStat(path string) (time.Time, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
