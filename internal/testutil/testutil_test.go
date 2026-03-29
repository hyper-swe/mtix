// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestNewTestStore_CreatesTempFileDB verifies that NewTestStore creates
// a working store backed by a temp-file SQLite database.
func TestNewTestStore_CreatesTempFileDB(t *testing.T) {
	s := testutil.NewTestStore(t)
	require.NotNil(t, s, "store should not be nil")
}

// TestNewTestStore_AutoCleanedOnTestEnd verifies that the store is
// automatically closed when the test completes (via t.Cleanup).
// This test simply verifies the store is usable — cleanup happens after.
func TestNewTestStore_AutoCleanedOnTestEnd(t *testing.T) {
	s := testutil.NewTestStore(t)
	require.NotNil(t, s)
	// The store cleanup is registered via t.Cleanup in NewTestStore.
	// We verify it's usable, and cleanup runs after this test function returns.
}

// TestMakeNode_DefaultValues verifies MakeNode creates a node with sensible defaults.
func TestMakeNode_DefaultValues(t *testing.T) {
	node := testutil.MakeNode(t)

	assert.Equal(t, "Test Node", node.Title)
	assert.Equal(t, "TEST", node.Project)
	assert.Equal(t, model.StatusOpen, node.Status)
	assert.Equal(t, model.PriorityMedium, node.Priority)
	assert.Equal(t, model.NodeTypeAuto, node.NodeType)
	assert.Equal(t, 1.0, node.Weight)
	assert.False(t, node.CreatedAt.IsZero())
	assert.False(t, node.UpdatedAt.IsZero())
}

// TestMakeNode_WithOptions verifies MakeNode applies functional options correctly.
func TestMakeNode_WithOptions(t *testing.T) {
	node := testutil.MakeNode(t,
		testutil.WithTitle("Custom Title"),
		testutil.WithParent("TEST-1"),
		testutil.WithStatus(model.StatusInProgress),
		testutil.WithPrompt("Do the thing"),
		testutil.WithPriority(model.PriorityCritical),
		testutil.WithDescription("A test description"),
		testutil.WithAcceptance("Tests pass"),
		testutil.WithLabels("backend", "urgent"),
	)

	assert.Equal(t, "Custom Title", node.Title)
	assert.Equal(t, "TEST-1", node.ParentID)
	assert.Equal(t, model.StatusInProgress, node.Status)
	assert.Equal(t, "Do the thing", node.Prompt)
	assert.Equal(t, model.PriorityCritical, node.Priority)
	assert.Equal(t, "A test description", node.Description)
	assert.Equal(t, "Tests pass", node.Acceptance)
	assert.Equal(t, []string{"backend", "urgent"}, node.Labels)
}

// TestFixedClock_ReturnsDeterministicTime verifies FixedClock always returns the same time.
func TestFixedClock_ReturnsDeterministicTime(t *testing.T) {
	expected := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := testutil.FixedClock(expected)

	for i := 0; i < 10; i++ {
		got := clock()
		assert.Equal(t, expected, got, "clock call %d should return fixed time", i)
	}
}
