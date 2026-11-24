// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-294 (F2, F3): a sync-applied transition or claim re-derives the node
// itself and then every ancestor in the same transaction, as the local paths
// do (FR-5.2, FR-5.3, FR-5.7), in both rollup modes.

func syncChain(t *testing.T, weighted bool) (*sqlite.Store, *sql.DB) {
	t.Helper()
	s, raw := mutationTestStore(t)
	s.SetProgressWeightedSource(func() bool { return weighted })
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
	markEventsPushed(t, raw)
	return s, raw
}

func syncProgress(t *testing.T, raw *sql.DB, ids ...string) []float64 {
	t.Helper()
	out := make([]float64, 0, len(ids))
	for _, id := range ids {
		var p float64
		require.NoError(t, raw.QueryRow(`SELECT progress FROM nodes WHERE id = ?`, id).Scan(&p))
		out = append(out, p)
	}
	return out
}

func foreignTransition(t *testing.T, raw *sql.DB, id string, from, to model.Status) *model.SyncEvent {
	t.Helper()
	return foreignWorkflowEvent(t, id, model.OpTransitionStatus, transition(from, to), nextLamport(t, raw), "")
}

// nextLamport returns a Lamport stamp above every event held, so the foreign
// event wins the workflow register.
func nextLamport(t *testing.T, raw *sql.DB) int64 {
	t.Helper()
	var l int64
	require.NoError(t, raw.QueryRow(`SELECT COALESCE(MAX(lamport_clock), 0) FROM sync_events`).Scan(&l))
	return l + 10
}

// A synced done on an intermediate node whose child is open credits neither
// the node nor its ancestors.
func TestSyncApply_DoneOnIntermediateWithOpenChild_NoCredit(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s, raw := syncChain(t, weighted)
		require.NoError(t, s.ClaimNode(context.Background(), "MTIX-1.1.1", "agent-a"))
		markEventsPushed(t, raw)
		pullEvents(t, s, []*model.SyncEvent{foreignTransition(t, raw, "MTIX-1.1", model.StatusOpen, model.StatusDone)})

		require.Equal(t, []float64{0, 0}, syncProgress(t, raw, "MTIX-1.1", "MTIX-1"), "weighted=%v", weighted)
	}
}

// A synced reopen of a done leaf drops its credit and its ancestors'.
func TestSyncApply_ReopenDoneLeaf_DropsCreditUpTheChain(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s, raw := syncChain(t, weighted)
		ctx := context.Background()
		require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1.1", "agent-a"))
		require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1.1", model.StatusDone, "d", "agent-a"))
		require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
		require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusDone, "d", "agent-a"))
		markEventsPushed(t, raw)
		require.Equal(t, []float64{1, 1, 1}, syncProgress(t, raw, "MTIX-1.1.1", "MTIX-1.1", "MTIX-1"))

		pullEvents(t, s, []*model.SyncEvent{foreignTransition(t, raw, "MTIX-1.1.1", model.StatusDone, model.StatusOpen)})

		require.Equal(t, []float64{0, 0, 0}, syncProgress(t, raw, "MTIX-1.1.1", "MTIX-1.1", "MTIX-1"), "weighted=%v", weighted)
	}
}

// A synced claim of a done leaf drops its credit and its ancestors'.
func TestSyncApply_ClaimDoneLeaf_DropsCreditUpTheChain(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s, raw := syncChain(t, weighted)
		ctx := context.Background()
		require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1.1", "agent-a"))
		require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1.1", model.StatusDone, "d", "agent-a"))
		markEventsPushed(t, raw)
		require.Equal(t, []float64{1, 1}, syncProgress(t, raw, "MTIX-1.1.1", "MTIX-1.1"))

		claim := foreignWorkflowEvent(t, "MTIX-1.1.1", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"},
			nextLamport(t, raw), "")
		pullEvents(t, s, []*model.SyncEvent{claim})

		require.Equal(t, []float64{0, 0, 0}, syncProgress(t, raw, "MTIX-1.1.1", "MTIX-1.1", "MTIX-1"), "weighted=%v", weighted)
	}
}
