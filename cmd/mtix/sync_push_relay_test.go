// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func relayPushTick(t *testing.T, cfg tick.StackConfig) {
	t.Helper()
	stack, err := tick.Open(cfg)
	require.NoError(t, err)
	pass, err := stack.Pass(t.Context())
	require.NoError(t, err)
	require.Empty(t, pass.Ingest.Stalls)
	require.Zero(t, pass.Ingest.Quarantined)
}

// The push reads a gateway populated by a real relay tick. It retries both
// sides of the hub commit and the ordinary clone consumes the hub's log.
func TestPushLoop_RelayRetryAndFreshHubClone(t *testing.T) {
	ctx := t.Context()
	peer, gateway := movesTestStore(t), movesTestStore(t)
	n := movesNode("MTIX-1", "", "relay work")
	require.NoError(t, peer.CreateNode(ctx, n))
	require.NoError(t, peer.CreateNode(ctx, movesNode("MTIX-1.1", n.ID, "relay child")))
	original, err := peer.ReadPendingEvents(ctx, 3)
	require.NoError(t, err)
	require.Len(t, original, 2)
	cfg := tick.StackConfig{Store: peer, RelayDir: t.TempDir(), PeerID: "1111111111111111"}
	_, err = lifecycle.Init(lifecycle.InitRequest{
		RelayDir: cfg.RelayDir, RelayID: "01a0238b-d7d5-77cc-95c6-98a472ed7803",
		CreatedAt: n.CreatedAt, CreatedBy: cfg.PeerID, Projects: []metadata.Project{{Prefix: "MTIX", FirstEventHash: "aaaa"}},
	})
	require.NoError(t, err)
	relayPushTick(t, cfg)
	cfg.Store, cfg.PeerID = gateway, "2222222222222222"
	relayPushTick(t, cfg)
	hub := newFakePushHub()
	hub.failCalls = 1
	_, err = pushLoop(ctx, io.Discard, hub, gateway)
	require.ErrorContains(t, err, "hub unreachable")
	failAck := context.WithValue(ctx, markPushedFailKey{}, func([]string) error { return errors.New("lost ack") })
	_, err = pushLoop(failAck, io.Discard, hub, gateway)
	require.ErrorContains(t, err, "lost ack")
	tot, err := pushLoop(ctx, io.Discard, hub, gateway)
	require.NoError(t, err)
	require.Equal(t, 1, tot.present)
	require.Equal(t, 1, tot.inserted)
	require.Equal(t, original, hub.events)
	tot, err = pushLoop(ctx, io.Discard, hub, gateway)
	require.NoError(t, err)
	require.Zero(t, tot.batches)
	requireRelayClone(t, hub.events, peer, gateway)
}

func requireRelayClone(t *testing.T, events []*model.SyncEvent, peer, gateway *sqlite.Store) {
	t.Helper()
	clone := movesTestStore(t)
	pulled, _, _, err := checkThenCloneInto(t.Context(), clone, io.Discard, &fakeLateHub{pullEvents: events}, transport.PullCursor{}, 10)
	require.NoError(t, err)
	require.Equal(t, len(events), pulled)
	for _, e := range events {
		want, err := peer.GetNode(t.Context(), e.NodeID)
		require.NoError(t, err)
		for _, s := range []*sqlite.Store{gateway, clone} {
			n, err := s.GetNode(t.Context(), e.NodeID)
			require.NoError(t, err)
			require.Equal(t, want.UID, n.UID)
			require.Equal(t, want.Title, n.Title)
			require.Equal(t, want.ParentID, n.ParentID)
		}
	}
}
