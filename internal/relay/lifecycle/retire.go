// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"fmt"
	"regexp"

	"github.com/hyper-swe/mtix/internal/relay/metadata"
)

// peerIDPattern is the FR-21 §5.2 identity grammar, restated here
// because this package carries no dependency on the frame layer.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// RetirePeer drops a peer from every prune quorum (FR-21 §6.7).
//
// Without retirement a peer that vanished forever never acks, and its
// silence stalls pruning permanently — the relay grows without bound
// because of a machine nobody has seen in months. Retirement is the
// cure, and it is operator-only on purpose: no computation can tell a
// decommissioned laptop from one whose owner is on leave, and only one
// of those should stop holding history.
//
// It is idempotent. Retiring an already-retired peer is a no-op rather
// than an error, so an operator re-running the command after an
// interrupted session is not punished for it.
func RetirePeer(relayDir, peerID string) error {
	if !peerIDPattern.MatchString(peerID) {
		return fmt.Errorf("retire peer: peer id %q is malformed", peerID)
	}
	doc, err := metadata.Read(relayDir)
	if err != nil {
		return err
	}
	for _, p := range doc.RetiredPeers {
		if p == peerID {
			return nil
		}
	}
	doc.RetiredPeers = append(doc.RetiredPeers, peerID)
	return metadata.Rewrite(relayDir, doc)
}

// RejoinPeer clears a peer's retirement (FR-21 §6.7).
//
// A retired peer that returns finds its watermark below the prune floor
// and re-enters through bootstrap; that path calls this so the peer
// counts in the quorum again. Without it a peer could be retired once
// and then hold nothing forever — publishing real work while every
// writer ignored its acks and pruned history it still needed.
//
// Idempotent: rejoining a peer that was never retired is a no-op.
func RejoinPeer(relayDir, peerID string) error {
	if !peerIDPattern.MatchString(peerID) {
		return fmt.Errorf("rejoin peer: peer id %q is malformed", peerID)
	}
	doc, err := metadata.Read(relayDir)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(doc.RetiredPeers))
	for _, p := range doc.RetiredPeers {
		if p != peerID {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(doc.RetiredPeers) {
		return nil
	}
	doc.RetiredPeers = kept
	return metadata.Rewrite(relayDir, doc)
}

// RetiredSet renders a relay's retirement roster in the shape the
// pruner consumes.
func RetiredSet(doc *metadata.Relay) map[string]bool {
	if doc == nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(doc.RetiredPeers))
	for _, p := range doc.RetiredPeers {
		out[p] = true
	}
	return out
}
