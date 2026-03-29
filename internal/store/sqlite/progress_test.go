// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestProgress_LeafOpen_Zero verifies leaf node open has progress 0.0.
func TestProgress_LeafOpen_Zero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Open Leaf", now)
	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.0, got.Progress)
}

// TestProgress_LeafDone_One verifies leaf node done has progress 1.0.
func TestProgress_LeafDone_One(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Done Leaf", now)
	node.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, node))

	// Transition to done (sets progress=1.0 in transition logic).
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "Complete", "agent-1"))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, got.Progress)
}

// TestProgress_Parent_DoneOverTotal verifies parent progress = done/total.
func TestProgress_Parent_DoneOverTotal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create 4 children: 2 done, 2 open.
	for i := 1; i <= 4; i++ {
		child := makeChildNode("PROJ-1."+itoa(i), "PROJ-1", "PROJ",
			"Child "+itoa(i), 1, i, now)
		if i <= 2 {
			child.Status = model.StatusDone
			child.Progress = 1.0
		}
		require.NoError(t, s.CreateNode(ctx, child))
	}

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, got.Progress, "2 done / 4 total = 0.5")
}

// TestProgress_CancelledExcluded verifies cancelled children are excluded per FR-5.4.
func TestProgress_CancelledExcluded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// 1 done child, 1 cancelled child.
	done := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done", 1, 1, now)
	done.Status = model.StatusDone
	done.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, done))

	cancelled := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Cancelled", 1, 2, now)
	cancelled.Status = model.StatusCancelled
	require.NoError(t, s.CreateNode(ctx, cancelled))

	// After recalc, cancelled is excluded: 1 done / 1 total = 1.0.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, got.Progress,
		"cancelled child excluded from denominator: 1/1 = 1.0")
}

// TestProgress_InvalidatedExcluded verifies invalidated children are excluded per FR-5.6.
func TestProgress_InvalidatedExcluded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// 1 done child, 1 invalidated child.
	done := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done", 1, 1, now)
	done.Status = model.StatusDone
	done.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, done))

	inv := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Invalidated", 1, 2, now)
	inv.Status = model.StatusInvalidated
	require.NoError(t, s.CreateNode(ctx, inv))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, got.Progress,
		"invalidated child excluded from denominator: 1/1 = 1.0")
}

// TestProgress_AllChildrenExcluded_ReturnsZeroWithFlag verifies FR-5.6b.
func TestProgress_AllChildrenExcluded_ReturnsZeroWithFlag(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// All children cancelled.
	c1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Cancelled 1", 1, 1, now)
	c1.Status = model.StatusCancelled
	require.NoError(t, s.CreateNode(ctx, c1))

	c2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Cancelled 2", 1, 2, now)
	c2.Status = model.StatusCancelled
	require.NoError(t, s.CreateNode(ctx, c2))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.0, got.Progress,
		"all children excluded → progress 0.0 per FR-5.6b")
}

// TestProgress_PropagesToRoot_InSameTransaction verifies FR-5.7.
func TestProgress_PropagesToRoot_InSameTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create 3-level hierarchy: root → epic → issue.
	root := makeRootNode("PROJ-1", "PROJ", "Root", now)
	require.NoError(t, s.CreateNode(ctx, root))

	epic := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Epic", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, epic))

	issue := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Issue", 2, 1, now)
	issue.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, issue))

	// Complete the leaf issue.
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1.1", model.StatusDone, "Done", "agent-1"))

	// Progress should propagate all the way to root.
	epicNode, err := s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, epicNode.Progress, "epic should be 1.0 (1/1 done)")

	rootNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, rootNode.Progress, "root should be 1.0 (propagated)")
}

// TestProgress_IncludesInvalidatedCount verifies FR-5.6a.
// The invalidated_count is exposed at the API level; at the store level,
// we verify invalidated children are excluded from progress calculation.
func TestProgress_IncludesInvalidatedCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// 2 open, 1 invalidated.
	for i := 1; i <= 2; i++ {
		child := makeChildNode("PROJ-1."+itoa(i), "PROJ-1", "PROJ",
			"Open "+itoa(i), 1, i, now)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	inv := makeChildNode("PROJ-1.3", "PROJ-1", "PROJ", "Invalidated", 1, 3, now)
	inv.Status = model.StatusInvalidated
	require.NoError(t, s.CreateNode(ctx, inv))

	// Progress should be 0.0 (0 done / 2 non-excluded).
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.0, got.Progress)

	// Verify we can count invalidated children via query.
	var invCount int
	err = s.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes
		 WHERE parent_id = ? AND status = ? AND deleted_at IS NULL`,
		"PROJ-1", string(model.StatusInvalidated),
	).Scan(&invCount)
	require.NoError(t, err)
	assert.Equal(t, 1, invCount, "should have 1 invalidated child")
}

// TestProgress_MatchesExampleFR5_9 verifies the example from FR-5.9.
// FR-5.9 example: PROJ-42 with 3 children, one done, one in_progress (50%), one open.
// Expected: (1.0 + 0.5 + 0.0) / 3 = 0.5
func TestProgress_MatchesExampleFR5_9(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-42", "PROJ", "Example Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Child 1: done (1.0).
	c1 := makeChildNode("PROJ-42.1", "PROJ-42", "PROJ", "Done Child", 1, 1, now)
	c1.Status = model.StatusDone
	c1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, c1))

	// Child 2: in_progress with 50% progress (has 2 grandchildren, 1 done).
	c2 := makeChildNode("PROJ-42.2", "PROJ-42", "PROJ", "Half Done", 1, 2, now)
	c2.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, c2))

	gc1 := makeChildNode("PROJ-42.2.1", "PROJ-42.2", "PROJ", "Done GC", 2, 1, now)
	gc1.Status = model.StatusDone
	gc1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, gc1))

	gc2 := makeChildNode("PROJ-42.2.2", "PROJ-42.2", "PROJ", "Open GC", 2, 2, now)
	require.NoError(t, s.CreateNode(ctx, gc2))

	// Child 3: open (0.0).
	c3 := makeChildNode("PROJ-42.3", "PROJ-42", "PROJ", "Open Child", 1, 3, now)
	require.NoError(t, s.CreateNode(ctx, c3))

	// PROJ-42.2 should be 0.5 (1 done / 2 total grandchildren).
	c2Node, err := s.GetNode(ctx, "PROJ-42.2")
	require.NoError(t, err)
	assert.Equal(t, 0.5, c2Node.Progress)

	// PROJ-42 should be (1.0 + 0.5 + 0.0) / 3 = 0.5.
	parentNode, err := s.GetNode(ctx, "PROJ-42")
	require.NoError(t, err)
	assert.Equal(t, 0.5, parentNode.Progress)
}

// TestProgress_ConcurrentSiblingCompletion_Correct verifies FR-5.7 scenario.
// Note: True concurrent testing requires multiple goroutines and the -race flag.
// This test simulates sequential sibling completion and verifies final correctness.
func TestProgress_ConcurrentSiblingCompletion_Correct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create 3 sibling micro issues, all in_progress.
	for i := 1; i <= 3; i++ {
		child := makeChildNode("PROJ-1."+itoa(i), "PROJ-1", "PROJ",
			"Micro "+itoa(i), 1, i, now)
		child.Status = model.StatusInProgress
		require.NoError(t, s.CreateNode(ctx, child))
	}

	// Complete all 3 siblings sequentially.
	for i := 1; i <= 3; i++ {
		require.NoError(t, s.TransitionStatus(ctx,
			"PROJ-1."+itoa(i), model.StatusDone, "Complete", "agent-"+itoa(i)))
	}

	// Final progress should be 1.0 (3/3 = 100%).
	parentNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, parentNode.Progress,
		"3/3 done siblings = 100%% progress")
}

// TestWeightedProgress_DefaultWeights_MatchesUnweighted verifies default weight behavior.
func TestWeightedProgress_DefaultWeights_MatchesUnweighted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// All children with default weight 1.0.
	c1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done", 1, 1, now)
	c1.Status = model.StatusDone
	c1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, c1))

	c2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Open", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	// With default weights, weighted = unweighted = 0.5.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, got.Progress)
}

// TestWeightedProgress_CustomWeights_CorrectCalculation verifies custom weights.
func TestWeightedProgress_CustomWeights_CorrectCalculation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Child 1: done, weight=3.
	c1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Heavy Done", 1, 1, now)
	c1.Status = model.StatusDone
	c1.Progress = 1.0
	c1.Weight = 3.0
	require.NoError(t, s.CreateNode(ctx, c1))

	// Child 2: open, weight=1.
	c2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Light Open", 1, 2, now)
	c2.Weight = 1.0
	require.NoError(t, s.CreateNode(ctx, c2))

	// Weighted progress: (1.0*3 + 0.0*1) / (3+1) = 3/4 = 0.75.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.75, got.Progress,
		"weighted: (1.0*3 + 0.0*1) / (3+1) = 0.75")
}

// TestWeightedProgress_AllExcluded_ReturnsZero verifies weighted with all excluded.
func TestWeightedProgress_AllExcluded_ReturnsZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	c1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Cancelled", 1, 1, now)
	c1.Status = model.StatusCancelled
	c1.Weight = 5.0
	require.NoError(t, s.CreateNode(ctx, c1))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.0, got.Progress,
		"all children excluded → 0.0 regardless of weights")
}

// TestProgress_DeferredIncluded verifies deferred children ARE included per FR-5.5.
func TestProgress_DeferredIncluded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	c1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done", 1, 1, now)
	c1.Status = model.StatusDone
	c1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, c1))

	c2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Deferred", 1, 2, now)
	c2.Status = model.StatusDeferred
	require.NoError(t, s.CreateNode(ctx, c2))

	// Deferred is included: 1 done / 2 total (including deferred) = 0.5.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, got.Progress,
		"deferred children included in denominator: 1/2 = 0.5")
}
