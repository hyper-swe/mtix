// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/peerid"
)

// relayCloneStampPrefix keys the publisher epoch a snapshot stamped for one
// peer. It lives apart from relay_ingest_cursor on purpose: the cursor decides
// which records are applied and must move exactly as it did before snapshots
// stamped epochs, while the stamp only feeds the continuity check (MTIX-265).
const relayCloneStampPrefix = "meta.relay.clone_stamp_epoch."

// SeedRelayCloneCursor is what `relay clone` records for one peer: the cursor,
// persisted as base always wrote it (epoch 0, so the store advances it on the
// first poll), and the stamped epoch beside it. Both are written in ONE
// transaction, so a failure leaves neither (MTIX-265). The stamp is written only
// when the seed took effect, that is when the cursor now holds the seeded
// relay sequence; a re-clone onto a cursor already further along keeps the stamp
// that belongs to that cursor.
func (s *Store) SeedRelayCloneCursor(ctx context.Context, peerID string, rs uint64, epoch uint16) error {
	if !peerid.Valid(peerID) {
		return fmt.Errorf("seed relay clone cursor: peer id %q is malformed: %w", peerID, model.ErrInvalidInput)
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if advErr := advanceRelayIngestCursor(ctx, tx, peerID, RelayIngestPosition{RS: rs}); advErr != nil {
			return advErr
		}
		var held int64
		// Read the cursor back inside the transaction to learn whether the seed took effect.
		if qErr := tx.QueryRowContext(ctx, `SELECT rs FROM relay_ingest_cursor WHERE peer_id = ?`,
			peerID).Scan(&held); qErr != nil {
			return fmt.Errorf("read seeded cursor: %w", qErr)
		}
		// #nosec G115 -- held is a stored non-negative relay sequence.
		if uint64(held) != rs {
			return nil
		}
		// Upsert one meta row keyed by peer; the value is the stamped epoch.
		if _, exErr := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			relayCloneStampPrefix+peerID, strconv.FormatUint(uint64(epoch), 10)); exErr != nil {
			return fmt.Errorf("write clone stamp: %w", exErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("seed relay clone cursor: %w", err)
	}
	return nil
}

// RelayCloneStampEpoch returns the epoch a snapshot stamped for a peer, or
// zero when none was stamped. A stored value outside the frame's range is
// refused rather than read as zero.
func (s *Store) RelayCloneStampEpoch(ctx context.Context, peerID string) (uint16, error) {
	if !peerid.Valid(peerID) {
		return 0, fmt.Errorf("relay clone stamp: peer id %q is malformed: %w", peerID, model.ErrInvalidInput)
	}
	var raw string
	// Read the stamped epoch for this peer, if a clone recorded one.
	err := s.readDB.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`,
		relayCloneStampPrefix+peerID).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read relay clone stamp: %w", err)
	}
	v, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil {
		return 0, fmt.Errorf("relay clone stamp %q is not an integer: %w", raw, model.ErrInvalidInput)
	}
	return relayEpoch("clone stamp epoch", v)
}
