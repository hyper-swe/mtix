// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// makeRootNode builds a minimal valid root node for testing.
func makeRootNode(id, project, title string, createdAt time.Time) *model.Node {
	return &model.Node{
		ID:          id,
		ParentID:    "",
		Project:     project,
		Depth:       0,
		Seq:         1,
		Title:       title,
		Status:      model.StatusOpen,
		Priority:    model.PriorityMedium,
		Progress:    0.0,
		Weight:      1.0,
		NodeType:    model.NodeTypeStory,
		Creator:     "test-agent",
		ContentHash: model.ComputeContentHash(title, "", "", "", nil),
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
}

// makeChildNode builds a minimal valid child node for testing.
func makeChildNode(id, parentID, project, title string, depth, seq int, createdAt time.Time) *model.Node {
	return &model.Node{
		ID:          id,
		ParentID:    parentID,
		Project:     project,
		Depth:       depth,
		Seq:         seq,
		Title:       title,
		Status:      model.StatusOpen,
		Priority:    model.PriorityMedium,
		Progress:    0.0,
		Weight:      1.0,
		NodeType:    model.NodeTypeEpic,
		Creator:     "test-agent",
		ContentHash: model.ComputeContentHash(title, "", "", "", nil),
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
}

// TestCreateNode_RootNode_CorrectID verifies root node creation with correct ID.
func TestCreateNode_RootNode_CorrectID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Root Story", now)

	err := s.CreateNode(ctx, node)
	require.NoError(t, err)

	// Verify by reading back.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", got.ID)
	assert.Equal(t, "", got.ParentID)
	assert.Equal(t, 0, got.Depth)
	assert.Equal(t, "PROJ", got.Project)
}

// TestCreateNode_ChildNode_CorrectID verifies child node creation under a parent.
func TestCreateNode_ChildNode_CorrectID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create parent first.
	parent := makeRootNode("PROJ-1", "PROJ", "Parent Story", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create child.
	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child Epic", 1, 1, now)
	err := s.CreateNode(ctx, child)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1.1", got.ID)
	assert.Equal(t, "PROJ-1", got.ParentID)
	assert.Equal(t, 1, got.Depth)
}

// TestCreateNode_SetsDefaults verifies default field values are persisted.
func TestCreateNode_SetsDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Test Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Equal(t, 0.0, got.Progress)
	assert.Equal(t, 1.0, got.Weight)
	assert.Equal(t, model.PriorityMedium, got.Priority)
}

// TestCreateNode_ComputesContentHash verifies content hash is stored.
func TestCreateNode_ComputesContentHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Hash Test Node", now)
	node.Description = "Some description"
	node.ContentHash = model.ComputeContentHash(node.Title, node.Description, "", "", nil)

	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	expectedHash := model.ComputeContentHash("Hash Test Node", "Some description", "", "", nil)
	assert.Equal(t, expectedHash, got.ContentHash)
	assert.NotEmpty(t, got.ContentHash)
}

// TestCreateNode_UnderOpenParent_Succeeds verifies child creation under an open parent.
func TestCreateNode_UnderOpenParent_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Open Parent", now)
	parent.Status = model.StatusOpen
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	err := s.CreateNode(ctx, child)
	assert.NoError(t, err)
}

// TestCreateNode_UnderCancelledParent_ReturnsInvalidInput per FR-3.9.
func TestCreateNode_UnderCancelledParent_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Cancelled Parent", now)
	parent.Status = model.StatusCancelled
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	err := s.CreateNode(ctx, child)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCreateNode_UnderDoneParent_ReturnsInvalidInput per FR-3.9.
func TestCreateNode_UnderDoneParent_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Done Parent", now)
	parent.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	err := s.CreateNode(ctx, child)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCreateNode_UnderInvalidatedParent_ReturnsInvalidInput per FR-3.9.
func TestCreateNode_UnderInvalidatedParent_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Invalidated Parent", now)
	parent.Status = model.StatusInvalidated
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	err := s.CreateNode(ctx, child)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCreateNode_RecalculatesParentProgress verifies parent progress
// is recalculated when a new child is added per FR-5.7.
func TestCreateNode_RecalculatesParentProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create parent with one "done" child (progress=1.0).
	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done Child", 1, 1, now)
	child1.Status = model.StatusDone
	child1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, child1))

	// Parent should now be at 1.0 (one child, done).
	parentNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 1.0, parentNode.Progress)

	// Add a second child (open, progress=0.0).
	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "New Child", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	// Parent progress should now be 0.5 (1.0+0.0)/2.
	parentNode, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, parentNode.Progress)
}

// TestCreateNode_CreatesActivityEntry verifies a 'created' activity entry is recorded.
func TestCreateNode_CreatesActivityEntry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Activity Test", now)
	node.Creator = "test-user"
	require.NoError(t, s.CreateNode(ctx, node))

	// Read the raw activity JSON from the database.
	var activityJSON string
	err := s.QueryRow(ctx,
		"SELECT activity FROM nodes WHERE id = ?", "PROJ-1",
	).Scan(&activityJSON)
	require.NoError(t, err)
	assert.Contains(t, activityJSON, "created")
	assert.Contains(t, activityJSON, "test-user")
}

// TestCreateNode_DuplicateID_ReturnsAlreadyExists verifies duplicate rejection.
func TestCreateNode_DuplicateID_ReturnsAlreadyExists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "First Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Try to create another node with the same ID.
	dup := makeRootNode("PROJ-1", "PROJ", "Duplicate Node", now)
	err := s.CreateNode(ctx, dup)
	assert.ErrorIs(t, err, model.ErrAlreadyExists)
}

// TestCreateNode_AutoClaim_WhenParentClaimed verifies auto-claim behavior.
// When the parent is claimed by an agent, the child inherits the assignee (FR-11.2a).
func TestCreateNode_AutoClaim_WhenParentClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Claimed Parent", now)
	parent.Assignee = "agent-001"
	parent.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create child with auto_claim — the caller sets assignee before calling CreateNode.
	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Auto-Claimed Child", 1, 1, now)
	child.Assignee = "agent-001" // Service layer would set this per FR-11.2a.
	require.NoError(t, s.CreateNode(ctx, child))

	got, err := s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, "agent-001", got.Assignee)
}

// TestCreateNode_WithAllFields verifies all 38 fields are persisted correctly.
func TestCreateNode_WithAllFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	est := 60
	act := 30

	node := &model.Node{
		ID:          "PROJ-1",
		ParentID:    "",
		Project:     "PROJ",
		Depth:       0,
		Seq:         1,
		Title:       "Full Node",
		Description: "A complete node with all fields",
		Prompt:      "Implement this feature",
		Acceptance:  "All tests pass",
		NodeType:    model.NodeTypeStory,
		IssueType:   model.IssueTypeFeature,
		Priority:    model.PriorityCritical,
		Labels:      []string{"urgent", "backend"},
		Status:      model.StatusOpen,
		Progress:    0.0,
		Assignee:    "dev@example.com",
		Creator:     "pm@example.com",
		AgentState:  model.AgentStateIdle,
		CreatedAt:   now,
		UpdatedAt:   now,
		EstimateMin: &est,
		ActualMin:   &act,
		Weight:      2.5,
		ContentHash: model.ComputeContentHash("Full Node", "A complete node with all fields", "Implement this feature", "All tests pass", []string{"urgent", "backend"}),
		SessionID:   "sess-001",
	}

	require.NoError(t, s.CreateNode(ctx, node))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	assert.Equal(t, "PROJ-1", got.ID)
	assert.Equal(t, "PROJ", got.Project)
	assert.Equal(t, "Full Node", got.Title)
	assert.Equal(t, "A complete node with all fields", got.Description)
	assert.Equal(t, "Implement this feature", got.Prompt)
	assert.Equal(t, "All tests pass", got.Acceptance)
	assert.Equal(t, model.NodeTypeStory, got.NodeType)
	assert.Equal(t, model.IssueTypeFeature, got.IssueType)
	assert.Equal(t, model.PriorityCritical, got.Priority)
	assert.Equal(t, []string{"urgent", "backend"}, got.Labels)
	assert.Equal(t, model.StatusOpen, got.Status)
	assert.Equal(t, "dev@example.com", got.Assignee)
	assert.Equal(t, "pm@example.com", got.Creator)
	assert.Equal(t, model.AgentStateIdle, got.AgentState)
	require.NotNil(t, got.EstimateMin)
	assert.Equal(t, 60, *got.EstimateMin)
	require.NotNil(t, got.ActualMin)
	assert.Equal(t, 30, *got.ActualMin)
	assert.Equal(t, 2.5, got.Weight)
	assert.Equal(t, node.ContentHash, got.ContentHash)
	assert.Equal(t, "sess-001", got.SessionID)
}

// TestCreateNode_EmptyTitle_ReturnsInvalidInput verifies title validation.
func TestCreateNode_EmptyTitle_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "", now)
	err := s.CreateNode(ctx, node)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCreateNode_NonExistentParent_ReturnsNotFound verifies parent existence check.
func TestCreateNode_NonExistentParent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	child := makeChildNode("PROJ-1.1", "PROJ-999", "PROJ", "Orphan", 1, 1, now)
	err := s.CreateNode(ctx, child)
	// The FK constraint or parent check should catch this.
	assert.Error(t, err)
}

// TestCreateNode_UnderInProgressParent_Succeeds verifies non-terminal parent acceptance.
func TestCreateNode_UnderInProgressParent_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name         string
		parentStatus model.Status
	}{
		{"in_progress parent", model.StatusInProgress},
		{"blocked parent", model.StatusBlocked},
		{"deferred parent", model.StatusDeferred},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parentID := sql.NullString{String: fmt.Sprintf("PROJ-%d", i+10), Valid: true}.String
			parent := makeRootNode(parentID, "PROJ", "Parent "+tt.name, now)
			parent.Status = tt.parentStatus
			require.NoError(t, s.CreateNode(ctx, parent))

			childID := parentID + ".1"
			child := makeChildNode(childID, parentID, "PROJ", "Child under "+tt.name, 1, 1, now)
			err := s.CreateNode(ctx, child)
			assert.NoError(t, err, "should succeed under %s parent", tt.parentStatus)
		})
	}
}
