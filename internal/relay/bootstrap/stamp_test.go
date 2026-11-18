// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const peerC = "aaaaaaaaaaaaaaaa"

// stampStore is a fake store slice for StampPositions.
type stampStore struct {
	ingest   map[string]sqlite.RelayIngestPosition
	push     sqlite.RelayPushPosition
	stamp    map[string]uint16
	ingErr   error
	stampErr error
	pushErr  error
}

func (s stampStore) RelayIngestCursor(_ context.Context, peer string) (sqlite.RelayIngestPosition, error) {
	return s.ingest[peer], s.ingErr
}

func (s stampStore) RelayCloneStampEpoch(_ context.Context, peer string) (uint16, error) {
	return s.stamp[peer], s.stampErr
}

func (s stampStore) RelayPushCursor(context.Context) (sqlite.RelayPushPosition, error) {
	return s.push, s.pushErr
}

func TestStampPositions_OwnStreamIsItsPublishedHead(t *testing.T) {
	st := stampStore{
		ingest: map[string]sqlite.RelayIngestPosition{peerB: {SegmentNo: 4, RS: 9, PubEpoch: 3}},
		push:   sqlite.RelayPushPosition{PubEpoch: 2, NextRS: 8},
	}
	got, err := bootstrap.StampPositions(context.Background(), st, peerA, []string{peerA, peerB})
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{peerA: 7, peerB: 9}, got.Positions, "own stream = NextRS-1, not the empty ingest cursor")
	require.Equal(t, map[string]uint16{peerA: 2, peerB: 3}, got.Epochs)
}

func TestStampPositions_NeverPublished_StampsNothingForOwnStream(t *testing.T) {
	st := stampStore{push: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1}}
	got, err := bootstrap.StampPositions(context.Background(), st, peerA, []string{peerB})
	require.NoError(t, err)
	require.NotContains(t, got.Positions, peerA)
	require.Equal(t, uint64(0), got.Positions[peerB])
}

func TestStampPositions_AfterResetBeforeFirstPublish_StampsTheEpochAtZero(t *testing.T) {
	st := stampStore{push: sqlite.RelayPushPosition{PubEpoch: 2, NextRS: 1}}
	got, err := bootstrap.StampPositions(context.Background(), st, peerA, []string{peerB})
	require.NoError(t, err)
	require.Contains(t, got.Positions, peerA)
	require.Equal(t, uint64(0), got.Positions[peerA])
	require.Equal(t, uint16(2), got.Epochs[peerA], "the epoch tells a clone the older epoch lies inside the snapshot")
}

func TestStampPositions_StoreErrors_ArePropagatedWrapped(t *testing.T) {
	boom := errors.New("boom")
	_, err := bootstrap.StampPositions(context.Background(), stampStore{ingErr: boom}, peerA, []string{peerB})
	require.ErrorIs(t, err, boom)
	_, err = bootstrap.StampPositions(context.Background(), stampStore{pushErr: boom}, peerA, nil)
	require.ErrorIs(t, err, boom)
}

func TestSnapshot_PubEpochs_RoundTripAndRefuseMalformedPeer(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	req := exportReq(relayDir, st)
	req.Epochs = map[string]uint16{peerA: 2, peerB: 1}
	res, err := bootstrap.ExportSnapshot(context.Background(), req)
	require.NoError(t, err)
	imported, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
		Store: newStore(t), Path: res.Path, Options: sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge},
	})
	require.NoError(t, err)
	require.Equal(t, req.Epochs, imported.Epochs)

	req.Epochs = map[string]uint16{"NOT A PEER": 1}
	_, err = bootstrap.ExportSnapshot(context.Background(), req)
	require.Error(t, err)
}

func TestStampPositions_UnpolledClone_StampsTheCloneStampEpoch(t *testing.T) {
	st := stampStore{
		ingest: map[string]sqlite.RelayIngestPosition{
			peerB: {RS: 2},                            // seeded by a clone, no poll yet: epoch 0
			peerC: {SegmentNo: 3, RS: 9, PubEpoch: 4}, // already polled: the cursor's epoch rules
		},
		stamp: map[string]uint16{peerB: 2, peerC: 7},
		push:  sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1},
	}
	got, err := bootstrap.StampPositions(context.Background(), st, peerA, []string{peerB, peerC})
	require.NoError(t, err)
	require.Equal(t, uint16(2), got.Epochs[peerB])
	require.Equal(t, uint16(4), got.Epochs[peerC], "a moved cursor is never overridden by the stamp")
	require.Equal(t, uint64(2), got.Positions[peerB])
}

func TestStampPositions_CloneStampReadFails_ReturnsTheError(t *testing.T) {
	st := stampStore{stampErr: errors.New("boom")}
	_, err := bootstrap.StampPositions(context.Background(), st, peerA, []string{peerB})
	require.ErrorContains(t, err, "read clone stamp")
}
