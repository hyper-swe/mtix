// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-294 (F14): releasing a claim changes a node's status, so UnclaimNode
// re-derives the node and then every ancestor in the same transaction
// (FR-5.2, FR-5.3, FR-5.7), in both rollup modes. A claimed intermediate
// credits nothing; once unclaimed with every descendant done it reads 1.0
// and credits its parent.
func TestUnclaimNode_ClaimedIntermediateOverDoneLeaf_RederivesAncestors(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s := rollupStore(t, weighted)
		ctx := context.Background()
		rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
		require.NoError(t, s.ClaimNode(ctx, "PROJ-1.1", "agent-1"))
		claimAndDone(t, s, "PROJ-1.1.1")
		require.Less(t, rollupProgress(t, s, "PROJ-1"), 1.0, "weighted=%v claimed mid is not complete", weighted)

		require.NoError(t, s.UnclaimNode(ctx, "PROJ-1.1", "released", "agent-1"))

		assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1.1"), "weighted=%v", weighted)
		assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1"), "weighted=%v", weighted)
		res, err := s.Verify(ctx)
		require.NoError(t, err)
		assert.True(t, res.ProgressOK, "weighted=%v errors=%v", weighted, res.Errors)
	}
}

// A synced unclaim re-derives the node and its ancestors as the local path
// does, in both rollup modes (F14, FR-5.7).
func TestSyncApply_UnclaimIntermediateOverDoneLeaf_RederivesAncestors(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s, raw := syncChain(t, weighted)
		ctx := context.Background()
		require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
		require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1.1", "agent-a"))
		require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1.1", model.StatusDone, "d", "agent-a"))
		markEventsPushed(t, raw)
		require.Less(t, syncProgress(t, raw, "MTIX-1")[0], 1.0, "weighted=%v claimed mid is not complete", weighted)
		require.Equal(t, []float64{1, 1}, syncProgress(t, raw, "MTIX-1.1.1", "MTIX-1.1"))

		unclaim := foreignWorkflowEvent(t, "MTIX-1.1", model.OpUnclaim, &model.UnclaimPayload{}, nextLamport(t, raw), "")
		applyOnStore(t, s, unclaim)

		require.Equal(t, []float64{1, 1, 1}, syncProgress(t, raw, "MTIX-1.1.1", "MTIX-1.1", "MTIX-1"), "weighted=%v", weighted)
		res, err := s.Verify(ctx)
		require.NoError(t, err)
		assert.True(t, res.ProgressOK, "weighted=%v errors=%v", weighted, res.Errors)
	}
}

// applyOnStore applies one event through Store.IdempotentApply, which carries
// the store's progress.weighted switch; the package-level IdempotentApply the
// pullEvents helper uses has no store and always rolls up in count mode.
func applyOnStore(t *testing.T, s *sqlite.Store, e *model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return s.IdempotentApply(ctx, tx, e) }))
}

// partialTree builds MTIX-1 over a done leaf MTIX-1.1 and an open node
// MTIX-1.2 holding three leaves, two done. The two rollup modes then differ
// at the root: FR-5.3 count mode credits 1/2 (1.2 is not done), FR-5.8
// weighted mode credits (1 + 2/3) / 2.
func partialTree(t *testing.T, weighted bool) (*sqlite.Store, *sql.DB) {
	t.Helper()
	s, raw := mutationTestStore(t)
	s.SetProgressWeightedSource(func() bool { return weighted })
	ctx := context.Background()
	for _, n := range [][2]string{
		{"MTIX-1", ""}, {"MTIX-1.1", "MTIX-1"}, {"MTIX-1.2", "MTIX-1"},
		{"MTIX-1.2.1", "MTIX-1.2"}, {"MTIX-1.2.2", "MTIX-1.2"}, {"MTIX-1.2.3", "MTIX-1.2"},
	} {
		mustCreateNode(t, s, n[0], n[1])
	}
	for _, id := range []string{"MTIX-1.1", "MTIX-1.2.1", "MTIX-1.2.2"} {
		require.NoError(t, s.ClaimNode(ctx, id, "agent-a"))
		require.NoError(t, s.TransitionStatus(ctx, id, model.StatusDone, "d", "agent-a"))
	}
	markEventsPushed(t, raw)
	return s, raw
}

// F15: the replica path honours progress.weighted. A synced reopen and a
// synced claim of a done leaf, with partial progress under the root, give
// different roots in the two modes; a rollup pinned to count mode on the
// replica path fails the weighted case.
func TestSyncApply_PartialProgress_RollupModeFollowsConfig(t *testing.T) {
	events := []struct {
		name string
		make func(t *testing.T, raw *sql.DB) *model.SyncEvent
	}{
		{"transition", func(t *testing.T, raw *sql.DB) *model.SyncEvent {
			return foreignTransition(t, raw, "MTIX-1.2.2", model.StatusDone, model.StatusOpen)
		}},
		{"claim", func(t *testing.T, raw *sql.DB) *model.SyncEvent {
			return foreignWorkflowEvent(t, "MTIX-1.2.2", model.OpClaim,
				&model.ClaimPayload{AgentID: "agent-b"}, nextLamport(t, raw), "")
		}},
	}
	modes := []struct {
		weighted bool
		wantRoot float64
	}{{false, 0.5}, {true, (1.0 + 1.0/3.0) / 2.0}}
	for _, ev := range events {
		for _, m := range modes {
			s, raw := partialTree(t, m.weighted)
			applyOnStore(t, s, ev.make(t, raw))

			got := syncProgress(t, raw, "MTIX-1.2", "MTIX-1")
			assert.InDelta(t, 1.0/3.0, got[0], 1e-9, "%s weighted=%v", ev.name, m.weighted)
			assert.InDelta(t, m.wantRoot, got[1], 1e-9, "%s weighted=%v", ev.name, m.weighted)
			res, err := s.Verify(context.Background())
			require.NoError(t, err)
			assert.True(t, res.ProgressOK, "%s weighted=%v errors=%v", ev.name, m.weighted, res.Errors)
		}
	}
}
