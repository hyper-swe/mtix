// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"database/sql"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func gatewayStore(t *testing.T, now func() time.Time) *sqlite.Store {
	t.Helper()
	s := testutil.NewTestStore(t).(*sqlite.Store)
	s.SetClock(now)
	return s
}

func gatewayNode(t *testing.T, s *sqlite.Store, id string, seq int) *model.SyncEvent {
	t.Helper()
	n := testutil.MakeNode(t, testutil.WithTitle(id))
	n.ID, n.Seq = id, seq
	require.NoError(t, s.CreateNode(t.Context(), n))
	events, err := s.ReadPendingEvents(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	return events[0]
}

func gatewayTick(t *testing.T, cfg tick.StackConfig) tick.Result {
	t.Helper()
	stack, err := tick.Open(cfg)
	require.NoError(t, err)
	result, err := stack.Pass(t.Context())
	require.NoError(t, err)
	require.Empty(t, result.Ingest.Stalls)
	require.Zero(t, result.Ingest.Quarantined)
	return result
}

func hubApply(t *testing.T, s *sqlite.Store, event *model.SyncEvent) {
	t.Helper()
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
		return s.IdempotentApply(t.Context(), tx, event)
	}))
}

func requireGatewayEvents(t *testing.T, s *sqlite.Store, want ...string) {
	t.Helper()
	rows, err := s.ReadRelayJournalSince(t.Context(), 0, 10)
	require.NoError(t, err)
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.Event.EventID)
	}
	require.ElementsMatch(t, want, ids)
}

// TestRelayGate_HybridGateway_ForwardsBothWaysAndAppliesDuplicateOnce exercises
// the hub push queue and the shared-directory tick (FR-21 §11.7, MTIX-64.9.2).
func TestRelayGate_HybridGateway_ForwardsBothWaysAndAppliesDuplicateOnce(t *testing.T) {
	now := testutil.FixedClock(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
	hub, gateway, peer := gatewayStore(t, now), gatewayStore(t, now), gatewayStore(t, now)
	g := tick.StackConfig{Store: gateway, PeerID: "0123456789abcdef", RelayDir: t.TempDir(), Now: now}
	p := tick.StackConfig{Store: peer, PeerID: "fedcba9876543210", RelayDir: g.RelayDir, Now: now}
	_, err := lifecycle.Init(lifecycle.InitRequest{
		RelayDir: g.RelayDir, RelayID: "01a0238b-d7d5-77cc-95c6-98a472ed7803",
		CreatedAt: now(), CreatedBy: g.PeerID,
		Projects: []metadata.Project{{Prefix: "TEST", FirstEventHash: "aaaa"}},
	})
	require.NoError(t, err)
	fromHub := gatewayNode(t, hub, "TEST-1", 1)
	fromPeer := gatewayNode(t, peer, "TEST-2", 2)
	hubApply(t, gateway, fromHub)
	gatewayTick(t, g)
	gatewayTick(t, p)
	requireGatewayEvents(t, peer, fromHub.EventID, fromPeer.EventID)
	// The peer republishes its journal, including the original hub event.
	gatewayTick(t, p)
	pass := gatewayTick(t, g)
	require.Equal(t, 2, pass.Ingest.Applied)
	requireGatewayEvents(t, gateway, fromHub.EventID, fromPeer.EventID)

	// Drain the same projection used by sync push into the offline hub.
	outgoing, err := gateway.ReadPendingEvents(t.Context(), 10)
	require.NoError(t, err)
	for _, event := range outgoing {
		hubApply(t, hub, event)
	}
	requireGatewayEvents(t, hub, fromHub.EventID, fromPeer.EventID)
}
