// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestDecompose_ValidChildren_AllCreated verifies batch child creation.
func TestDecompose_ValidChildren_AllCreated(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	children := []service.DecomposeInput{
		{Title: "Child 1"},
		{Title: "Child 2"},
		{Title: "Child 3"},
	}

	ids, err := svc.Decompose(ctx, parent.ID, children, "admin")
	require.NoError(t, err)
	require.Len(t, ids, 3)

	// Verify all children exist in store.
	for _, id := range ids {
		got, err := s.GetNode(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, parent.ID, got.ParentID)
	}
}

// TestDecompose_AtomicTransaction_PartialFailureRollsBack verifies atomicity.
// If any child fails validation, none should be created.
func TestDecompose_AtomicTransaction_PartialFailureRollsBack(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	children := []service.DecomposeInput{
		{Title: "Valid Child"},
		{Title: ""}, // Invalid — empty title.
	}

	_, err = svc.Decompose(ctx, parent.ID, children, "admin")
	assert.ErrorIs(t, err, model.ErrInvalidInput)

	// Verify no children were created (validation fails upfront).
	got, err := s.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Len(t, got, 0)
}

// TestDecompose_TerminalParent_ReturnsInvalidInput verifies FR-3.9 rejection.
func TestDecompose_TerminalParent_ReturnsInvalidInput(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent To Cancel",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Cancel the parent.
	require.NoError(t, s.CancelNode(ctx, parent.ID, "descoping", "admin", false))

	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{
		{Title: "Should Fail"},
	}, "admin")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "cancelled")
}

// TestDecompose_GeneratesSequentialIDs verifies IDs are sequential.
func TestDecompose_GeneratesSequentialIDs(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	children := []service.DecomposeInput{
		{Title: "First"},
		{Title: "Second"},
		{Title: "Third"},
	}

	ids, err := svc.Decompose(ctx, parent.ID, children, "admin")
	require.NoError(t, err)
	require.Len(t, ids, 3)

	// IDs should be sequential: PROJ-1.1, PROJ-1.2, PROJ-1.3
	assert.Equal(t, parent.ID+".1", ids[0])
	assert.Equal(t, parent.ID+".2", ids[1])
	assert.Equal(t, parent.ID+".3", ids[2])
}

// TestDecompose_BroadcastsEvents verifies event broadcasting.
func TestDecompose_BroadcastsEvents(t *testing.T) {
	svc, _, bc := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)
	bc.Reset()

	children := []service.DecomposeInput{
		{Title: "Child 1"},
		{Title: "Child 2"},
	}

	_, err = svc.Decompose(ctx, parent.ID, children, "admin")
	require.NoError(t, err)

	events := bc.Events()
	// Expect: node.created for each child + progress.changed for parent.
	require.Len(t, events, 3)

	var createdCount int
	var progressCount int
	for _, e := range events {
		switch e.Type {
		case service.EventNodeCreated:
			createdCount++
		case service.EventProgressChanged:
			progressCount++
		}
	}
	assert.Equal(t, 2, createdCount)
	assert.Equal(t, 1, progressCount)
}

// TestDecompose_RecalculatesParentProgress verifies progress rollup.
func TestDecompose_RecalculatesParentProgress(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{
		{Title: "Child 1"},
		{Title: "Child 2"},
	}, "admin")
	require.NoError(t, err)

	// Parent progress should be 0.0 (both children are open).
	got, err := s.GetNode(ctx, parent.ID)
	require.NoError(t, err)
	assert.InDelta(t, 0.0, got.Progress, 0.001)
}

// TestDecompose_WithPromptAndAcceptance verifies rich format input.
func TestDecompose_WithPromptAndAcceptance(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	children := []service.DecomposeInput{
		{
			Title:      "Rich Child",
			Prompt:     "Do something specific",
			Acceptance: "It is done when...",
			Labels:     []string{"urgent"},
		},
	}

	ids, err := svc.Decompose(ctx, parent.ID, children, "admin")
	require.NoError(t, err)
	require.Len(t, ids, 1)

	got, err := s.GetNode(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, "Rich Child", got.Title)
	assert.Equal(t, "Do something specific", got.Prompt)
	assert.Equal(t, "It is done when...", got.Acceptance)
	assert.Contains(t, got.Labels, "urgent")
	assert.NotEmpty(t, got.ContentHash)
}

// TestDecompose_NonExistentParent_ReturnsNotFound verifies parent existence check.
func TestDecompose_NonExistentParent_ReturnsNotFound(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.Decompose(ctx, "NONEXISTENT-1", []service.DecomposeInput{
		{Title: "Orphan"},
	}, "admin")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDecompose_EmptyChildren_ReturnsError verifies empty input rejection.
func TestDecompose_EmptyChildren_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	// Create a parent to decompose under.
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{}, "admin")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestDecompose_ChildTitleTooLong_ReturnsError verifies title length validation.
func TestDecompose_ChildTitleTooLong_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Parent", Creator: "admin",
	})
	require.NoError(t, err)

	longTitle := ""
	for i := 0; i < model.MaxTitleLength+1; i++ {
		longTitle += "x"
	}
	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{
		{Title: longTitle},
	}, "admin")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestDecompose_AutoClaim_WhenConfigured verifies FR-11.2a in decompose context.
func TestDecompose_AutoClaim_WhenConfigured(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{AutoClaimEnabled: true}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, bc, cfg, slog.Default(), fixedClock(now))
	ctx := context.Background()

	// Create and claim parent.
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Claimed Parent",
		Creator: "agent-1",
	})
	require.NoError(t, err)
	require.NoError(t, s.ClaimNode(ctx, parent.ID, "agent-1"))

	// Decompose under claimed parent.
	ids, err := svc.Decompose(ctx, parent.ID, []service.DecomposeInput{
		{Title: "Auto-Claim Child"},
	}, "agent-1")
	require.NoError(t, err)
	require.Len(t, ids, 1)

	// Child should be auto-claimed (in_progress, assigned to agent-1).
	got, err := s.GetNode(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
	assert.Equal(t, "agent-1", got.Assignee)
}
