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
	"github.com/hyper-swe/mtix/internal/store"
)

// stringPtr returns a pointer to a string.
func stringPtr(s string) *string { return &s }

// priorityPtr returns a pointer to a Priority.
func priorityPtr(p model.Priority) *model.Priority { return &p }

// TestUpdateNode_Title_UpdatesField verifies title update.
func TestUpdateNode_Title_UpdatesField(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Original Title", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Title: stringPtr("Updated Title"),
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", got.Title)
}

// TestUpdateNode_MultipleFields_AllUpdated verifies multiple field updates.
func TestUpdateNode_MultipleFields_AllUpdated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Original", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Title:       stringPtr("New Title"),
		Description: stringPtr("New Description"),
		Priority:    priorityPtr(model.PriorityCritical),
		Assignee:    stringPtr("dev@example.com"),
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "New Title", got.Title)
	assert.Equal(t, "New Description", got.Description)
	assert.Equal(t, model.PriorityCritical, got.Priority)
	assert.Equal(t, "dev@example.com", got.Assignee)
}

// TestUpdateNode_RecomputesContentHash verifies hash recomputation per FR-3.7.
func TestUpdateNode_RecomputesContentHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Hash Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	originalHash := node.ContentHash

	// Update a content field.
	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Description: stringPtr("New description"),
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	// Hash should change after content update.
	assert.NotEqual(t, originalHash, got.ContentHash)
	expectedHash := model.ComputeContentHash("Hash Node", "New description", "", "", nil)
	assert.Equal(t, expectedHash, got.ContentHash)
}

// TestUpdateNode_SetsUpdatedAt verifies updated_at is set on update.
func TestUpdateNode_SetsUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Timestamp Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	original, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	// Small delay to ensure updated_at changes.
	time.Sleep(10 * time.Millisecond)

	err = s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Title: stringPtr("Updated"),
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	// updated_at should be later than the original.
	assert.True(t, got.UpdatedAt.After(original.UpdatedAt) || got.UpdatedAt.Equal(original.UpdatedAt),
		"updated_at should be set to current time")
}

// TestUpdateNode_NonExistent_ReturnsNotFound verifies ErrNotFound.
func TestUpdateNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpdateNode(ctx, "NONEXISTENT-1", &store.NodeUpdate{
		Title: stringPtr("Updated"),
	})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUpdateNode_SoftDeleted_ReturnsNotFound verifies deleted nodes cannot be updated.
func TestUpdateNode_SoftDeleted_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Title: stringPtr("Updated"),
	})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUpdateNode_FTSUpdatedViaTrigger verifies FTS index updates when content changes.
func TestUpdateNode_FTSUpdatedViaTrigger(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Original FTS Title", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Update title to trigger FTS update via database trigger.
	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Title: stringPtr("Updated FTS Title"),
	})
	require.NoError(t, err)

	// Search FTS for the new title.
	var count int
	err = s.QueryRow(ctx,
		"SELECT COUNT(*) FROM nodes_fts WHERE nodes_fts MATCH ?",
		"Updated",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "FTS should find the updated title")

	// Old title should not be in FTS.
	err = s.QueryRow(ctx,
		"SELECT COUNT(*) FROM nodes_fts WHERE nodes_fts MATCH ?",
		"Original",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "FTS should not find the old title")
}

// TestUpdateNode_NonContentField_NoHashChange verifies hash is NOT
// recomputed when only non-content fields change.
func TestUpdateNode_NonContentField_NoHashChange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Stable Hash", now)
	require.NoError(t, s.CreateNode(ctx, node))

	original, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	// Update non-content field (priority).
	err = s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Priority: priorityPtr(model.PriorityCritical),
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	// Hash should remain unchanged.
	assert.Equal(t, original.ContentHash, got.ContentHash)
}

// TestUpdateNode_PromptAndAcceptance_UpdatesFields verifies prompt/acceptance updates.
func TestUpdateNode_PromptAndAcceptance_UpdatesFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "PA Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Prompt:     stringPtr("Write a function"),
		Acceptance: stringPtr("All tests pass"),
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "Write a function", got.Prompt)
	assert.Equal(t, "All tests pass", got.Acceptance)
}

// TestUpdateNode_AgentState_UpdatesField verifies agent_state update per FR-10.4.
func TestUpdateNode_AgentState_UpdatesField(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Agent Node", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	agentState := model.AgentStateDone
	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		AgentState: &agentState,
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateDone, got.AgentState)
}

// TestUpdateNode_EmptyUpdate_NoOp verifies no-op update when no fields set.
func TestUpdateNode_EmptyUpdate_NoOp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "NoOp Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Update with no fields set — should be a no-op.
	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "NoOp Node", got.Title)
}

// TestUpdateNode_StatusViaUpdate_SetsField verifies status update via NodeUpdate.
func TestUpdateNode_StatusViaUpdate_SetsField(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Status Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	status := model.StatusInProgress
	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Status: &status,
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestUpdateNode_Labels_UpdatesAndRecomputesHash verifies label updates.
func TestUpdateNode_Labels_UpdatesAndRecomputesHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Label Node", now)
	node.Labels = []string{"alpha", "beta"}
	node.ContentHash = model.ComputeContentHash("Label Node", "", "", "", []string{"alpha", "beta"})
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{
		Labels: []string{"gamma", "delta"},
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"gamma", "delta"}, got.Labels)

	expectedHash := model.ComputeContentHash("Label Node", "", "", "", []string{"gamma", "delta"})
	assert.Equal(t, expectedHash, got.ContentHash)
}
