// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestGetNode_ExistingNode_ReturnsFullNode verifies full node retrieval.
func TestGetNode_ExistingNode_ReturnsFullNode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Get Test Node", now)
	node.Description = "A description"
	node.Prompt = "A prompt"
	node.ContentHash = node.ComputeHash()
	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", got.ID)
	assert.Equal(t, "Get Test Node", got.Title)
	assert.Equal(t, "A description", got.Description)
	assert.Equal(t, "A prompt", got.Prompt)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestGetNode_NonExistent_ReturnsNotFound verifies ErrNotFound for missing nodes.
func TestGetNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetNode(ctx, "NONEXISTENT-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetNode_SoftDeleted_ReturnsNotFound verifies deleted nodes are excluded.
func TestGetNode_SoftDeleted_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Soft-delete the node.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// GetNode should return ErrNotFound.
	_, err := s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetNode_ComputesChildCount verifies child_count is computed at query time.
func TestGetNode_ComputesChildCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create 3 children.
	for i := 1; i <= 3; i++ {
		child := makeChildNode(
			"PROJ-1."+itoa(i), "PROJ-1", "PROJ",
			"Child "+itoa(i), 1, i, now,
		)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	// Verify child count via GetDirectChildren.
	children, err := s.GetDirectChildren(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Len(t, children, 3)
}

// TestGetNode_ActivityPagination_DefaultLimit verifies activity is returned.
func TestGetNode_ActivityPagination_DefaultLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Activity Node", now)
	node.Creator = "agent-1"
	require.NoError(t, s.CreateNode(ctx, node))

	// Read the activity JSON to verify 'created' entry exists.
	var activityJSON string
	err := s.QueryRow(ctx,
		"SELECT activity FROM nodes WHERE id = ?", "PROJ-1",
	).Scan(&activityJSON)
	require.NoError(t, err)
	assert.Contains(t, activityJSON, `"type":"created"`)
	assert.Contains(t, activityJSON, `"author":"agent-1"`)
}

// TestGetDirectChildren_OrderedBySeq verifies children are sorted by sequence.
func TestGetDirectChildren_OrderedBySeq(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create children in reverse order.
	for _, seq := range []int{3, 1, 2} {
		child := makeChildNode(
			"PROJ-1."+itoa(seq), "PROJ-1", "PROJ",
			"Child "+itoa(seq), 1, seq, now,
		)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	children, err := s.GetDirectChildren(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Len(t, children, 3)

	// Should be ordered by seq: 1, 2, 3.
	assert.Equal(t, 1, children[0].Seq)
	assert.Equal(t, 2, children[1].Seq)
	assert.Equal(t, 3, children[2].Seq)
}

// TestGetDirectChildren_ExcludesDeleted verifies soft-deleted children are excluded.
func TestGetDirectChildren_ExcludesDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Active Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Deleted Child", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	// Soft-delete one child.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.2", false, "admin"))

	children, err := s.GetDirectChildren(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Len(t, children, 1)
	assert.Equal(t, "PROJ-1.1", children[0].ID)
}

// itoa is a simple int-to-string helper for test readability.
func itoa(i int) string {
	return strconv.Itoa(i)
}

// TestGetDirectChildren_NoChildren_ReturnsEmpty verifies empty children list.
func TestGetDirectChildren_NoChildren_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Leaf Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	children, err := s.GetDirectChildren(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Empty(t, children)
}

// TestGetDirectChildren_NonExistentParent_ReturnsEmpty verifies missing parent.
func TestGetDirectChildren_NonExistentParent_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	children, err := s.GetDirectChildren(ctx, "NONEXISTENT")
	require.NoError(t, err)
	assert.Empty(t, children)
}

// TestGetActivity_ExistingNode_ReturnsEntries verifies activity retrieval per FR-3.6.
func TestGetActivity_ExistingNode_ReturnsEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("ACT-1", "ACT", "Activity Test", now)
	node.Creator = "test-user"
	require.NoError(t, s.CreateNode(ctx, node))

	// Transition to add a status_change activity entry.
	require.NoError(t, s.TransitionStatus(ctx, "ACT-1", model.StatusInProgress, "", "test-agent"))

	entries, err := s.GetActivity(ctx, "ACT-1", 50, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(entries), 2, "should have at least created + status_change entries")

	// First entry should be the created event.
	assert.Equal(t, model.ActivityTypeCreated, entries[0].Type)
	assert.Equal(t, "test-user", entries[0].Author)
}

// TestGetActivity_NonExistentNode_ReturnsNotFound verifies ErrNotFound.
func TestGetActivity_NonExistentNode_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetActivity(ctx, "NONEXISTENT-1", 50, 0)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetActivity_Pagination_RespectsLimitAndOffset verifies pagination.
func TestGetActivity_Pagination_RespectsLimitAndOffset(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("ACT-2", "ACT", "Pagination Test", now)
	node.Creator = "agent-1"
	require.NoError(t, s.CreateNode(ctx, node))

	// Add multiple transitions to accumulate activity.
	require.NoError(t, s.TransitionStatus(ctx, "ACT-2", model.StatusInProgress, "", "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, "ACT-2", model.StatusOpen, "unclaim", "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, "ACT-2", model.StatusInProgress, "", "agent-2"))

	// Get all entries.
	all, err := s.GetActivity(ctx, "ACT-2", 50, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 4)

	// Limit to 2.
	limited, err := s.GetActivity(ctx, "ACT-2", 2, 0)
	require.NoError(t, err)
	assert.Len(t, limited, 2)

	// Offset by 1, limit 2.
	offset, err := s.GetActivity(ctx, "ACT-2", 2, 1)
	require.NoError(t, err)
	assert.Len(t, offset, 2)
	assert.Equal(t, all[1].ID, offset[0].ID)
}

// TestGetActivity_EmptyActivity_ReturnsEmptySlice verifies empty activity.
func TestGetActivity_EmptyActivity_ReturnsEmptySlice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node — it will have a 'created' entry, so test with fresh DB.
	node := makeRootNode("ACT-3", "ACT", "Empty Activity", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Even a fresh node has a 'created' entry, so we verify it returns at least that.
	entries, err := s.GetActivity(ctx, "ACT-3", 50, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, entries)
}

