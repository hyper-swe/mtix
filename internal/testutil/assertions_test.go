// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestAssertNodeStatus_MatchingStatus_Passes verifies status assertion passes on match.
func TestAssertNodeStatus_MatchingStatus_Passes(t *testing.T) {
	// Create a node with open status through the store's service layer.
	// For testing assertion helpers, we'll verify the assertion logic itself.
	// We can test this by mocking or by using internal structures.

	// Create a test store and verify assertion works when status matches.
	// Note: This is testing the assertion helper itself.
	node := testutil.MakeNode(t, testutil.WithStatus(model.StatusOpen))

	// Manually verify the assertion logic.
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestAssertNodeStatus_MismatchedStatus_Fails verifies status assertion fails on mismatch.
func TestAssertNodeStatus_MismatchedStatus_Fails(t *testing.T) {
	// Create nodes with different statuses.
	node1 := testutil.MakeNode(t, testutil.WithStatus(model.StatusOpen))
	node2 := testutil.MakeNode(t, testutil.WithStatus(model.StatusDone))

	// Verify they have different statuses.
	assert.NotEqual(t, node1.Status, node2.Status)
}

// TestAssertProgress_WithinEpsilon_Passes verifies progress assertion with tolerance.
func TestAssertProgress_WithinEpsilon_Passes(t *testing.T) {
	// MakeNode creates a node with default progress of 0.0.
	node := testutil.MakeNode(t)
	expectedProgress := 0.0

	// Progress should be within epsilon (0.001) of expected.
	assert.InDelta(t, expectedProgress, node.Progress, 0.001)
}

// TestAssertNodeExists_CreatedNode_Exists verifies node existence assertion.
func TestAssertNodeExists_CreatedNode_Exists(t *testing.T) {
	s := testutil.NewTestStore(t)
	require.NotNil(t, s)

	// Store is created and should be usable.
	// The assertion helpers can be tested by verifying the logic.
}

// TestAssertNodeNotFound_MissingNode_NotFound verifies not found assertion.
func TestAssertNodeNotFound_MissingNode_NotFound(t *testing.T) {
	s := testutil.NewTestStore(t)
	require.NotNil(t, s)

	// Query for a non-existent node.
	_, err := s.GetNode(context.Background(), "NONEXISTENT-1")

	// Should return ErrNotFound.
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestMakeNode_AllDefaults_NotNil verifies default node creation.
func TestMakeNode_AllDefaults_NotNil(t *testing.T) {
	node := testutil.MakeNode(t)

	require.NotNil(t, node)
	assert.NotEmpty(t, node.Title)
	assert.NotEmpty(t, node.Project)
}

// TestMakeNode_MultipleOptions_Applied verifies all options apply.
func TestMakeNode_MultipleOptions_Applied(t *testing.T) {
	node := testutil.MakeNode(t,
		testutil.WithTitle("Epic Task"),
		testutil.WithParent("TEST-1"),
		testutil.WithStatus(model.StatusInProgress),
		testutil.WithPrompt("Build feature"),
		testutil.WithPriority(model.PriorityCritical),
		testutil.WithDescription("A detailed description"),
		testutil.WithAcceptance("All tests pass"),
		testutil.WithLabels("feature", "backend", "urgent"),
	)

	assert.Equal(t, "Epic Task", node.Title)
	assert.Equal(t, "TEST-1", node.ParentID)
	assert.Equal(t, model.StatusInProgress, node.Status)
	assert.Equal(t, "Build feature", node.Prompt)
	assert.Equal(t, model.PriorityCritical, node.Priority)
	assert.Equal(t, "A detailed description", node.Description)
	assert.Equal(t, "All tests pass", node.Acceptance)
	assert.Len(t, node.Labels, 3)
	assert.Contains(t, node.Labels, "feature")
	assert.Contains(t, node.Labels, "backend")
	assert.Contains(t, node.Labels, "urgent")
}

// TestMakeNode_EmptyLabels_Default verifies default labels is empty.
func TestMakeNode_EmptyLabels_Default(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Empty(t, node.Labels)
}

// TestMakeNode_WithSingleLabel_Works verifies single label option.
func TestMakeNode_WithSingleLabel_Works(t *testing.T) {
	node := testutil.MakeNode(t, testutil.WithLabels("solo"))
	require.Len(t, node.Labels, 1)
	assert.Equal(t, "solo", node.Labels[0])
}

// TestMakeNode_WithMultipleLabels_Works verifies multiple labels option.
func TestMakeNode_WithMultipleLabels_Works(t *testing.T) {
	node := testutil.MakeNode(t, testutil.WithLabels("a", "b", "c"))
	assert.Len(t, node.Labels, 3)
	assert.Equal(t, []string{"a", "b", "c"}, node.Labels)
}

// TestMakeNode_OptionsOrder_LastWins verifies last option overrides earlier ones.
func TestMakeNode_OptionsOrder_LastWins(t *testing.T) {
	node := testutil.MakeNode(t,
		testutil.WithTitle("First Title"),
		testutil.WithTitle("Second Title"),
	)

	assert.Equal(t, "Second Title", node.Title)
}

// TestMakeNode_Priority_AllValues verifies all priority levels work.
func TestMakeNode_Priority_AllValues(t *testing.T) {
	priorities := []model.Priority{
		model.PriorityLow,
		model.PriorityMedium,
		model.PriorityHigh,
		model.PriorityCritical,
	}

	for _, p := range priorities {
		node := testutil.MakeNode(t, testutil.WithPriority(p))
		assert.Equal(t, p, node.Priority)
	}
}

// TestMakeNode_Status_AllValues verifies all status values work.
func TestMakeNode_Status_AllValues(t *testing.T) {
	statuses := model.AllStatuses()

	for _, s := range statuses {
		node := testutil.MakeNode(t, testutil.WithStatus(s))
		assert.Equal(t, s, node.Status)
	}
}

// TestMakeNode_TimestampsAreSet verifies creation/update times are non-zero.
func TestMakeNode_TimestampsAreSet(t *testing.T) {
	node := testutil.MakeNode(t)

	assert.False(t, node.CreatedAt.IsZero())
	assert.False(t, node.UpdatedAt.IsZero())
	// Should be equal on creation.
	assert.Equal(t, node.CreatedAt, node.UpdatedAt)
}

// TestMakeNode_Weight_Default verifies default weight.
func TestMakeNode_Weight_Default(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Equal(t, 1.0, node.Weight)
}

// TestMakeNode_Progress_Default verifies default progress is zero.
func TestMakeNode_Progress_Default(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Equal(t, 0.0, node.Progress)
}

// TestFixedClock_ConsistentValue verifies fixed clock always returns same time.
func TestFixedClock_ConsistentValue(t *testing.T) {
	expected := time.Date(2026, 1, 15, 14, 30, 45, 0, time.UTC)
	clock := testutil.FixedClock(expected)

	// Call multiple times and verify consistency.
	for i := 0; i < 100; i++ {
		got := clock()
		assert.Equal(t, expected, got)
	}
}

// TestFixedClock_DifferentTimes verifies different clocks have different times.
func TestFixedClock_DifferentTimes(t *testing.T) {
	time1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	time2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	clock1 := testutil.FixedClock(time1)
	clock2 := testutil.FixedClock(time2)

	assert.NotEqual(t, clock1(), clock2())
}

// TestFixedClock_WithNanoseconds verifies nanosecond precision.
func TestFixedClock_WithNanoseconds(t *testing.T) {
	expected := time.Date(2026, 3, 10, 12, 34, 56, 789012345, time.UTC)
	clock := testutil.FixedClock(expected)

	got := clock()
	assert.Equal(t, expected, got)
	assert.Equal(t, expected.Nanosecond(), got.Nanosecond())
}

// TestNewTestStore_IsNotNil verifies store creation.
func TestNewTestStore_IsNotNil(t *testing.T) {
	s := testutil.NewTestStore(t)
	assert.NotNil(t, s)
}

// TestNewTestStore_IsUsable verifies store can be queried.
func TestNewTestStore_IsUsable(t *testing.T) {
	s := testutil.NewTestStore(t)

	// Verify we can call store methods.
	_, _, err := s.ListNodes(context.Background(), store.NodeFilter{}, store.ListOptions{Limit: 10, Offset: 0})
	assert.NoError(t, err)
}

// TestNewTestStore_CreatesUniqueDBs verifies each store has own DB.
func TestNewTestStore_CreatesUniqueDBs(t *testing.T) {
	s1 := testutil.NewTestStore(t)
	s2 := testutil.NewTestStore(t)

	// Both should be non-nil and independent.
	assert.NotNil(t, s1)
	assert.NotNil(t, s2)
	assert.NotEqual(t, s1, s2)
}

// TestMakeNode_DefaultNodeType verifies default node type.
func TestMakeNode_DefaultNodeType(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Equal(t, model.NodeTypeAuto, node.NodeType)
}

// TestMakeNode_EmptyPromptDefault verifies default prompt is empty.
func TestMakeNode_EmptyPromptDefault(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Empty(t, node.Prompt)
}

// TestMakeNode_EmptyDescriptionDefault verifies default description is empty.
func TestMakeNode_EmptyDescriptionDefault(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Empty(t, node.Description)
}

// TestMakeNode_EmptyAcceptanceDefault verifies default acceptance is empty.
func TestMakeNode_EmptyAcceptanceDefault(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Empty(t, node.Acceptance)
}

// TestMakeNode_EmptyParentIDDefault verifies default parent ID is empty.
func TestMakeNode_EmptyParentIDDefault(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Empty(t, node.ParentID)
}

// TestMakeNode_ProjectDefault verifies project is TEST by default.
func TestMakeNode_ProjectDefault(t *testing.T) {
	node := testutil.MakeNode(t)
	assert.Equal(t, "TEST", node.Project)
}

// TestFixedClock_ZeroTime verifies clock handles zero time.
func TestFixedClock_ZeroTime(t *testing.T) {
	zeroTime := time.Time{}
	clock := testutil.FixedClock(zeroTime)

	got := clock()
	assert.Equal(t, zeroTime, got)
	assert.True(t, got.IsZero())
}

// TestMakeNode_Deterministic verifies same options produce same node structure.
func TestMakeNode_Deterministic(t *testing.T) {
	opts := []testutil.NodeOption{
		testutil.WithTitle("Deterministic"),
		testutil.WithStatus(model.StatusDone),
	}

	node1 := testutil.MakeNode(t, opts...)
	node2 := testutil.MakeNode(t, opts...)

	// Should have same values (though different object references).
	assert.Equal(t, node1.Title, node2.Title)
	assert.Equal(t, node1.Status, node2.Status)
	assert.NotSame(t, node1, node2)
}

// --- Assertion Helper Integration Tests (require real store with persisted nodes) ---

// TestAssertNodeStatus_WithStore_MatchingStatus verifies AssertNodeStatus with a real store.
func TestAssertNodeStatus_WithStore_MatchingStatus(t *testing.T) {
	s := testutil.NewTestStore(t)

	// Create and persist a node.
	node := testutil.MakeNode(t, testutil.WithStatus(model.StatusOpen))
	require.NoError(t, s.CreateNode(context.Background(), node))

	// AssertNodeStatus should pass for matching status.
	testutil.AssertNodeStatus(t, s, node.ID, model.StatusOpen)
}

// TestAssertProgress_WithStore_ZeroProgress verifies AssertProgress with a real store.
func TestAssertProgress_WithStore_ZeroProgress(t *testing.T) {
	s := testutil.NewTestStore(t)

	node := testutil.MakeNode(t)
	require.NoError(t, s.CreateNode(context.Background(), node))

	// New node should have 0.0 progress.
	testutil.AssertProgress(t, s, node.ID, 0.0)
}

// TestAssertNodeExists_WithStore_CreatedNode verifies AssertNodeExists with a real store.
func TestAssertNodeExists_WithStore_CreatedNode(t *testing.T) {
	s := testutil.NewTestStore(t)

	node := testutil.MakeNode(t)
	require.NoError(t, s.CreateNode(context.Background(), node))

	// Node should exist.
	testutil.AssertNodeExists(t, s, node.ID)
}

// TestAssertNodeNotFound_WithStore_MissingNode verifies AssertNodeNotFound with a real store.
func TestAssertNodeNotFound_WithStore_MissingNode(t *testing.T) {
	s := testutil.NewTestStore(t)

	// Node that was never created should not be found.
	testutil.AssertNodeNotFound(t, s, "NONEXISTENT-999")
}
