// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// StampSource is the store slice that stamping positions needs.
type StampSource interface {
	RelayIngestCursor(ctx context.Context, peerID string) (sqlite.RelayIngestPosition, error)
	RelayPushCursor(ctx context.Context) (sqlite.RelayPushPosition, error)
	// RelayCloneStampEpoch is the epoch an imported snapshot stamped for a
	// peer; a clone that exports before its first poll still holds it here.
	RelayCloneStampEpoch(ctx context.Context, peerID string) (uint16, error)
}

// CloneSeeder is the store slice that seeds a clone's cursors.
type CloneSeeder interface {
	SeedRelayCloneCursor(ctx context.Context, peerID string, rs uint64, epoch uint16) error
}

// SeedCloneCursors records the positions an imported snapshot stamped: for each
// peer the cursor (as base always wrote it) and the stamped epoch beside it,
// in one transaction per peer. It is the single seeding path: `relay clone`
// calls it, and so do the tests that build a clone (MTIX-265).
func SeedCloneCursors(ctx context.Context, seeder CloneSeeder, res ImportResult) error {
	peers := make([]string, 0, len(res.Positions))
	for peer := range res.Positions {
		peers = append(peers, peer)
	}
	sort.Strings(peers)
	for _, peer := range peers {
		if err := seeder.SeedRelayCloneCursor(ctx, peer, res.Positions[peer], res.Epochs[peer]); err != nil {
			return fmt.Errorf("seed clone cursor for peer %s: %w", peer, err)
		}
	}
	return nil
}

// Stamp is the position vector a snapshot carries: for each peer, the relay
// sequence the exporting store holds and the publisher epoch it was read in.
type Stamp struct {
	Positions map[string]uint64
	Epochs    map[string]uint16
}

// StampPositions reads what the exporting store holds of every peer stream
// (MTIX-265). For another peer that is this store's ingest cursor.
//
// The stamp is an input to the clone's continuity check only: it says which
// holes lie inside the snapshot and so are not gaps. It never decides which
// records the clone applies; the clone replays every retained record, as it
// always has, so LWW judges them against the other streams' events and no
// newer value is reverted.
//
// For the exporter's OWN stream the ingest cursor is always empty, since a peer
// never ingests itself, but the store holds every event the peer has
// published, so the stamp is its published head (next relay sequence less one)
// in its publisher epoch. After a reset-peer and before the first publish in
// the new epoch nothing is published yet, but the epoch is stamped with
// sequence 0 so the older epochs, which the snapshot holds, are not read as
// missing. Stamping a zero position in epoch 1 would make a clone read the
// stream as a fresh reader and stall RELAY_GAP once retention pruned its
// oldest segment, though the snapshot already holds those events.
func StampPositions(ctx context.Context, src StampSource, self string, peers []string) (Stamp, error) {
	st := Stamp{Positions: make(map[string]uint64, len(peers)), Epochs: make(map[string]uint16, len(peers))}
	for _, peer := range peers {
		pos, err := src.RelayIngestCursor(ctx, peer)
		if err != nil {
			return Stamp{}, fmt.Errorf("read ingest cursor for peer %s: %w", peer, err)
		}
		epoch := pos.PubEpoch
		if pos.SegmentNo == 0 && pos.PubEpoch == 0 {
			// A clone seeds its cursor at epoch 0 and keeps the stamped epoch
			// apart; until its first poll moves the cursor the stamp is the
			// true epoch, and a clone of this export must inherit it.
			stamp, stampErr := src.RelayCloneStampEpoch(ctx, peer)
			if stampErr != nil {
				return Stamp{}, fmt.Errorf("read clone stamp for peer %s: %w", peer, stampErr)
			}
			epoch = stamp
		}
		st.Positions[peer], st.Epochs[peer] = pos.RS, epoch
	}
	push, err := src.RelayPushCursor(ctx)
	if err != nil {
		return Stamp{}, fmt.Errorf("read relay publish cursor: %w", err)
	}
	if push.NextRS > 1 || push.PubEpoch > 1 {
		st.Positions[self], st.Epochs[self] = push.NextRS-1, push.PubEpoch
	}
	return st, nil
}
