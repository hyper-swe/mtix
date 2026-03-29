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

// TestCancel_RequiresReason verifies mandatory cancel reason.
func TestCancel_RequiresReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Cancel Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.CancelNode(ctx, "PROJ-1", "", "pm-1", false)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCancel_NoChildren_CancelsNode verifies simple cancellation.
func TestCancel_NoChildren_CancelsNode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Cancel Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.CancelNode(ctx, "PROJ-1", "No longer needed", "pm-1", false)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)
	assert.NotNil(t, got.ClosedAt)
}

// TestCancel_WithCascade_CancelsDescendants verifies cascade cancellation.
func TestCancel_WithCascade_CancelsDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child 1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Child 2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	grandchild := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Grandchild", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, grandchild))

	// Cancel parent with cascade.
	err := s.CancelNode(ctx, "PROJ-1", "Entire feature descoped", "pm-1", true)
	require.NoError(t, err)

	// All descendants should be cancelled.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)

	got, err = s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)

	got, err = s.GetNode(ctx, "PROJ-1.2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)

	got, err = s.GetNode(ctx, "PROJ-1.1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)
}

// TestCancel_WithKeepChildren_ChildrenUnchanged verifies keep-children.
func TestCancel_WithKeepChildren_ChildrenUnchanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	// Cancel parent without cascade (keep-children).
	err := s.CancelNode(ctx, "PROJ-1", "Parent only", "pm-1", false)
	require.NoError(t, err)

	// Parent should be cancelled.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)

	// Child should remain open.
	got, err = s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestCancel_ExcludedFromProgressDenominator verifies FR-5.4.
func TestCancel_ExcludedFromProgressDenominator(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create 3 children: one done (1.0), one open (0.0), one to cancel.
	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done Child", 1, 1, now)
	child1.Status = model.StatusDone
	child1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Open Child", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	child3 := makeChildNode("PROJ-1.3", "PROJ-1", "PROJ", "To Cancel", 1, 3, now)
	require.NoError(t, s.CreateNode(ctx, child3))

	// Parent should be ~0.33 (1 done / 3 total).
	parentNode, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.InDelta(t, 1.0/3.0, parentNode.Progress, 0.01)

	// Cancel child3.
	require.NoError(t, s.CancelNode(ctx, "PROJ-1.3", "Descoped", "pm-1", false))

	// Parent should now be 0.5 (1 done / 2 non-cancelled).
	// Note: current progress implementation uses weighted average of all non-deleted children.
	// recalculateProgress excludes cancelled/invalidated children per FR-5.4.
	// Remaining: child1 (done, 1.0) + child2 (open, 0.0) = 1.0/2 = 0.5
	parentNode, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.InDelta(t, 0.5, parentNode.Progress, 0.01)
}

// TestCancel_AlreadyCancelled_Idempotent verifies idempotent cancel.
func TestCancel_AlreadyCancelled_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Cancel Node", now)
	node.Status = model.StatusCancelled
	require.NoError(t, s.CreateNode(ctx, node))

	// Cancel already-cancelled node should not error (via ValidateTransition idempotency).
	err := s.CancelNode(ctx, "PROJ-1", "Trying again", "pm-1", false)
	// The transition cancelled→cancelled is idempotent in our state machine check.
	// But CancelNode does validate the transition first. cancelled→cancelled = same status = nil.
	assert.NoError(t, err)
}

// TestCancel_NonExistent_ReturnsNotFound verifies ErrNotFound for missing nodes.
func TestCancel_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.CancelNode(ctx, "NONEXISTENT-1", "reason", "pm-1", false)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestCancel_DoneNode_ReturnsInvalidTransition verifies done->cancelled rejection.
func TestCancel_DoneNode_ReturnsInvalidTransition(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Done Node", now)
	node.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.CancelNode(ctx, "PROJ-1", "too late", "pm-1", false)
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestCancel_InProgressNode_Succeeds verifies in_progress can be cancelled.
func TestCancel_InProgressNode_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "In Progress Node", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.CancelNode(ctx, "PROJ-1", "Descoped", "pm-1", false)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)
	assert.NotNil(t, got.ClosedAt)
}

// TestCancel_CascadeSkipsTerminalDescendants verifies FR-6.3 cascade skip.
func TestCancel_CascadeSkipsTerminalDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// One child already done, one open.
	doneChild := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Done", 1, 1, now)
	doneChild.Status = model.StatusDone
	doneChild.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, doneChild))

	openChild := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Open", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, openChild))

	err := s.CancelNode(ctx, "PROJ-1", "Whole feature gone", "pm-1", true)
	require.NoError(t, err)

	// Done child should remain done (terminal status not overwritten).
	got, err := s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, got.Status)

	// Open child should be cancelled.
	got, err = s.GetNode(ctx, "PROJ-1.2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)
}

// TestCancel_WithParent_RecalculatesProgress verifies FR-5.7 on cancel.
func TestCancel_WithParent_RecalculatesProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	grandparent := makeRootNode("PROJ-1", "PROJ", "Grandparent", now)
	require.NoError(t, s.CreateNode(ctx, grandparent))

	parent := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Parent", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Child", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	// Cancel child; parent progress should recalculate.
	err := s.CancelNode(ctx, "PROJ-1.1.1", "not needed", "pm-1", false)
	require.NoError(t, err)

	// Parent has no non-cancelled children, progress should be 0.0 per FR-5.6b.
	got, err := s.GetNode(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, got.Progress, 0.01)
}
