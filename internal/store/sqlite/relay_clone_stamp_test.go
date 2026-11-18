// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

const stampPeer = "fedcba9876543210"

func TestRelayCloneStampEpoch_RoundTripsAndLeavesTheCursorAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.RelayCloneStampEpoch(ctx, stampPeer)
	require.NoError(t, err)
	require.Zero(t, got, "no stamp reads as epoch 0")

	require.NoError(t, s.SeedRelayCloneCursor(ctx, stampPeer, 0, 3))
	require.NoError(t, s.SeedRelayCloneCursor(ctx, stampPeer, 0, 4), "a later clone replaces the stamp")
	got, err = s.RelayCloneStampEpoch(ctx, stampPeer)
	require.NoError(t, err)
	require.Equal(t, uint16(4), got)

	pos, err := s.RelayIngestCursor(ctx, stampPeer)
	require.NoError(t, err)
	require.Zero(t, pos.PubEpoch, "the stamp never reaches the ingest cursor")
}

func TestRelayCloneStampEpoch_MalformedPeerIsRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.ErrorIs(t, s.SeedRelayCloneCursor(ctx, "bad id", 0, 1), model.ErrInvalidInput)
	_, err := s.RelayCloneStampEpoch(ctx, "bad id")
	require.ErrorIs(t, err, model.ErrInvalidInput)

}

// TestSeedRelayCloneCursor_FailedStampWriteLeavesNeitherWritten: the cursor and
// the stamp are one transaction, so when the stamp cannot be written the cursor
// is not left behind (MTIX-265). The failure is injected with a trigger that
// aborts the stamp's meta row.
func TestSeedRelayCloneCursor_FailedStampWriteLeavesNeitherWritten(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER block_clone_stamp BEFORE INSERT ON meta
		WHEN NEW.key LIKE 'meta.relay.clone_stamp_epoch.%'
		BEGIN SELECT RAISE(ABORT, 'injected stamp failure'); END`)
	require.NoError(t, err)

	err = s.SeedRelayCloneCursor(ctx, stampPeer, 5, 2)
	require.ErrorContains(t, err, "injected stamp failure")

	pos, err := s.RelayIngestCursor(ctx, stampPeer)
	require.NoError(t, err)
	require.Zero(t, pos.RS, "the cursor write rolled back with the stamp")
	got, err := s.RelayCloneStampEpoch(ctx, stampPeer)
	require.NoError(t, err)
	require.Zero(t, got)
}

// TestSeedRelayCloneCursor_ReCloneOntoAFurtherCursor_KeepsItsStamp: a re-clone
// whose position is below the cursor does not move the cursor, so it must not
// overwrite the stamp that belongs to that cursor.
func TestSeedRelayCloneCursor_ReCloneOntoAFurtherCursor_KeepsItsStamp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.SeedRelayCloneCursor(ctx, stampPeer, 9, 1))
	require.NoError(t, s.SeedRelayCloneCursor(ctx, stampPeer, 4, 3))

	pos, err := s.RelayIngestCursor(ctx, stampPeer)
	require.NoError(t, err)
	require.Equal(t, uint64(9), pos.RS)
	got, err := s.RelayCloneStampEpoch(ctx, stampPeer)
	require.NoError(t, err)
	require.Equal(t, uint16(1), got, "the stamp stays with the cursor it describes")
}
