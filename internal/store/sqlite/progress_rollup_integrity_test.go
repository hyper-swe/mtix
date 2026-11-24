// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-294: a progress rollup MUST NOT read complete while work remains
// (FR-5.2, FR-5.3, FR-5.7, FR-5.8).

func rollupStore(t *testing.T, weighted bool) *sqlite.Store {
	t.Helper()
	s := newTestStore(t)
	s.SetProgressWeightedSource(func() bool { return weighted })
	return s
}

func rollupChain(t *testing.T, s *sqlite.Store, ids ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	parent := ""
	for i, id := range ids {
		n := makeChildNode(id, parent, "PROJ", id, i, 1, now)
		require.NoError(t, s.CreateNode(ctx, n))
		parent = id
	}
}

func rollupProgress(t *testing.T, s *sqlite.Store, id string) float64 {
	t.Helper()
	n, err := s.GetNode(context.Background(), id)
	require.NoError(t, err)
	return n.Progress
}

func claimAndDone(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.ClaimNode(ctx, id, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusDone, "done", "agent-1"))
}

// Hazard 1 (default config, FR-5.3): the claimed child is not done, so the
// root is 0/1 although its only leaf descendant is done.
func TestRollup_DefaultConfig_ClaimedChildNotCreditedByGrandchild(t *testing.T) {
	s := rollupStore(t, false)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
	require.NoError(t, s.ClaimNode(ctx, "PROJ-1.1", "agent-1"))
	claimAndDone(t, s, "PROJ-1.1.1")

	assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1.1"), "child: 1/1 grandchildren done")
	assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1"), "root: child is in_progress, FR-5.3 gives 0/1")
}

// Hazard 1 (weighted on, F1/F9): root -> claimed child -> done grandchild.
// The child is in_progress, so the root must stay below 1.0 although the
// child's own progress (its only grandchild is done) is 1.0.
func TestRollup_Weighted_ClaimedChildNotCreditedByGrandchild(t *testing.T) {
	s := rollupStore(t, true)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
	require.NoError(t, s.ClaimNode(ctx, "PROJ-1.1", "agent-1"))
	claimAndDone(t, s, "PROJ-1.1.1")

	assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1.1"), "child: its only grandchild is done")
	assert.Less(t, rollupProgress(t, s, "PROJ-1"), 1.0, "root: the child is in_progress")
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1", model.StatusDone, "d", "agent-1"))
	assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1"), "settles once the child is done")
}

// Hazard 2: complete the only leaf, reopen it, claim it: credit is gone at
// each step on the leaf and on every ancestor (FR-5.2, FR-5.7).
func TestRollup_ReopenAndClaimResetLeafCredit(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s := rollupStore(t, weighted)
		ctx := context.Background()
		rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
		claimAndDone(t, s, "PROJ-1.1.1")
		require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1.1", model.StatusOpen, "reopen", "agent-1"))
		for _, id := range []string{"PROJ-1.1.1", "PROJ-1.1", "PROJ-1"} {
			assert.Equal(t, 0.0, rollupProgress(t, s, id), "after reopen %s weighted=%v", id, weighted)
		}
		require.NoError(t, s.ClaimNode(ctx, "PROJ-1.1.1", "agent-2"))
		for _, id := range []string{"PROJ-1.1.1", "PROJ-1.1", "PROJ-1"} {
			assert.Equal(t, 0.0, rollupProgress(t, s, id), "after claim %s weighted=%v", id, weighted)
		}
	}
}

// A stale stored credit on a claimed leaf (the claim itself must reset it).
func TestRollup_ClaimResetsStaleLeafCredit(t *testing.T) {
	s := rollupStore(t, false)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1", "PROJ-1.1")
	n := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "stale", 1, 2, time.Now().UTC())
	n.Progress = 1.0 // open leaf carrying credit, as an import or old build left it
	require.NoError(t, s.CreateNode(ctx, n))
	require.NoError(t, s.ClaimNode(ctx, "PROJ-1.2", "agent-1"))
	assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1.2"))
}

// Hazard 2b: completing an intermediate node whose child is still active
// must not credit it or its ancestors (both modes).
func TestRollup_DoneIntermediateWithActiveChild_NoCredit(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s := rollupStore(t, weighted)
		ctx := context.Background()
		rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
		require.NoError(t, s.ClaimNode(ctx, "PROJ-1.1.1", "agent-1"))
		claimAndDone(t, s, "PROJ-1.1")

		assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1.1"), "own credit weighted=%v", weighted)
		assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1"), "ancestor credit weighted=%v", weighted)

		require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1.1", model.StatusDone, "d", "agent-1"))
		assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1"), "settles once the child is done")
	}
}

// Hazard 5: an included unfinished zero-weight row beside done work.
func TestRollup_Weighted_ZeroWeightUnfinishedNeverReadsComplete(t *testing.T) {
	s := rollupStore(t, true)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1")
	now := time.Now().UTC()
	done := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "d", 1, 1, now)
	zero := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "z", 1, 2, now)
	zero.Weight = 0
	require.NoError(t, s.CreateNode(ctx, done))
	require.NoError(t, s.CreateNode(ctx, zero))
	claimAndDone(t, s, "PROJ-1.1")
	require.NoError(t, s.ClaimNode(ctx, "PROJ-1.2", "agent-1"))

	got := rollupProgress(t, s, "PROJ-1")
	assert.Less(t, got, 1.0, "unfinished zero-weight row must keep the parent below complete")
}

// Hazard 5, precision: a 2^54 weight must not round a real deficit away.
func TestRollup_Weighted_PrecisionNeverReadsComplete(t *testing.T) {
	s := rollupStore(t, true)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1")
	now := time.Now().UTC()
	big := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "big", 1, 1, now)
	big.Weight = math.Pow(2, 54)
	require.NoError(t, s.CreateNode(ctx, big))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "small", 1, 2, now)))
	claimAndDone(t, s, "PROJ-1.1")
	assert.Less(t, rollupProgress(t, s, "PROJ-1"), 1.0)
}

// Hazard 6: deleting the last live child leaves a done node at 1.0 (FR-5.2).
func TestRollup_DeleteLastChildKeepsDoneParentCredit(t *testing.T) {
	s := rollupStore(t, false)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
	claimAndDone(t, s, "PROJ-1.1.1")
	claimAndDone(t, s, "PROJ-1.1")
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.1.1", false, "agent-1"))
	assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1.1"), "done and childless is a done leaf")
	assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1"))
}

// FR-5.7: a failed ancestor progress write rolls the whole transition back is
// covered by the existing tx tests; here three sibling completions behind a
// barrier must leave every status and the root consistent.
func TestRollup_ConcurrentSiblingCompletionBehindBarrier(t *testing.T) {
	s := rollupStore(t, false)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1", "PROJ-1.1")
	now := time.Now().UTC()
	ids := []string{"PROJ-1.1.1", "PROJ-1.1.2", "PROJ-1.1.3"}
	for i, id := range ids {
		require.NoError(t, s.CreateNode(ctx, makeChildNode(id, "PROJ-1.1", "PROJ", id, 2, i+1, now)))
		require.NoError(t, s.ClaimNode(ctx, id, "agent-1"))
	}
	require.NoError(t, s.ClaimNode(ctx, "PROJ-1.1", "agent-1"))
	start := make(chan struct{})
	errs := make(chan error, len(ids))
	for _, id := range ids {
		go func() {
			<-start
			errs <- s.TransitionStatus(ctx, id, model.StatusDone, "d", "agent-1")
		}()
	}
	close(start)
	for range ids {
		require.NoError(t, <-errs)
	}
	for _, id := range ids {
		n, err := s.GetNode(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, model.StatusDone, n.Status)
	}
	assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1.1"))
	assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1"), "child still in_progress: FR-5.3 root is 0/1")
}

// F5: a done node whose children are all cancelled (FR-5.6b reads 0.0 for it)
// still counts as done for its parent (FR-5.3), in both modes.
func TestRollup_DoneOverOnlyCancelledChildren_CreditsParent(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s := rollupStore(t, weighted)
		ctx := context.Background()
		rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
		require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1.1", model.StatusCancelled, "n/a", "agent-1"))
		claimAndDone(t, s, "PROJ-1.1")

		assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1.1"), "FR-5.6b: all children excluded, weighted=%v", weighted)
		assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1"), "all work resolved, weighted=%v", weighted)
	}
}

// F7: every included child at weight 0 gives 0.0, never NaN or 1.0.
func TestRollup_Weighted_AllIncludedChildrenZeroWeight_IsZero(t *testing.T) {
	s := rollupStore(t, true)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1")
	for i, id := range []string{"PROJ-1.1", "PROJ-1.2"} {
		n := makeChildNode(id, "PROJ-1", "PROJ", id, 1, i+1, time.Now().UTC())
		n.Weight = 0
		require.NoError(t, s.CreateNode(ctx, n))
	}
	claimAndDone(t, s, "PROJ-1.1")
	assert.Equal(t, 0.0, rollupProgress(t, s, "PROJ-1"))
}

// F8: a partially complete child at small weight beside a huge done weight
// rounds to 1.0 in float math; the parent must stay below 1.0.
func TestRollup_Weighted_PartialChildNeverReadsComplete(t *testing.T) {
	s := rollupStore(t, true)
	ctx := context.Background()
	rollupChain(t, s, "PROJ-1")
	now := time.Now().UTC()
	big := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "big", 1, 1, now)
	big.Weight = math.Pow(2, 60)
	require.NoError(t, s.CreateNode(ctx, big))
	partial := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "partial", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, partial))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.2.1", "PROJ-1.2", "PROJ", "a", 2, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("PROJ-1.2.2", "PROJ-1.2", "PROJ", "b", 2, 2, now)))
	claimAndDone(t, s, "PROJ-1.1")
	claimAndDone(t, s, "PROJ-1.2.1") // PROJ-1.2 reads 0.5: partial
	assert.Equal(t, 0.5, rollupProgress(t, s, "PROJ-1.2"))
	assert.Less(t, rollupProgress(t, s, "PROJ-1"), 1.0)
}
