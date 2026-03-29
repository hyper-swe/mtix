// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestDecomposeDryRun_ValidChildren_ReturnsProposedNodes verifies dry-run
// returns proposed nodes without writing to the store.
func TestDecomposeDryRun_ValidChildren_ReturnsProposedNodes(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	children := []service.DecomposeInput{
		{Title: "Child A", Description: "Desc A"},
		{Title: "Child B", Description: "Desc B"},
		{Title: "Child C"},
	}

	proposed, err := svc.DecomposeDryRun(ctx, parent.ID, children)
	require.NoError(t, err)
	require.Len(t, proposed, 3)

	// Verify proposed IDs are sequential based on existing children.
	assert.Equal(t, parent.ID+".1", proposed[0].ID)
	assert.Equal(t, parent.ID+".2", proposed[1].ID)
	assert.Equal(t, parent.ID+".3", proposed[2].ID)

	// Verify titles and descriptions are carried through.
	assert.Equal(t, "Child A", proposed[0].Title)
	assert.Equal(t, "Desc A", proposed[0].Description)
	assert.Equal(t, "Child B", proposed[1].Title)
	assert.Equal(t, "Child C", proposed[2].Title)

	// Verify NO nodes were actually created.
	got, err := s.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Len(t, got, 0, "dry-run must not create any nodes")
}

// TestDecomposeDryRun_WithExistingSiblings_ContinuesSequence verifies that
// proposed IDs account for existing children.
func TestDecomposeDryRun_WithExistingSiblings_ContinuesSequence(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Create 2 real children first.
	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{
		{Title: "Existing 1"},
		{Title: "Existing 2"},
	}, "admin")
	require.NoError(t, err)

	// Dry-run should propose IDs starting after existing children.
	proposed, err := svc.DecomposeDryRun(ctx, parent.ID, []service.DecomposeInput{
		{Title: "New Child"},
	})
	require.NoError(t, err)
	require.Len(t, proposed, 1)
	assert.Equal(t, parent.ID+".3", proposed[0].ID)
}

// TestDecomposeDryRun_NonExistentParent_ReturnsNotFound verifies parent check.
func TestDecomposeDryRun_NonExistentParent_ReturnsNotFound(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.DecomposeDryRun(ctx, "NONEXISTENT-1", []service.DecomposeInput{
		{Title: "Orphan"},
	})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestDecomposeDryRun_TerminalParent_ReturnsInvalidInput verifies FR-3.9.
func TestDecomposeDryRun_TerminalParent_ReturnsInvalidInput(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent To Cancel",
		Creator: "admin",
	})
	require.NoError(t, err)
	require.NoError(t, s.CancelNode(ctx, parent.ID, "descoping", "admin", false))

	_, err = svc.DecomposeDryRun(ctx, parent.ID, []service.DecomposeInput{
		{Title: "Should Fail"},
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestDecomposeDryRun_InvalidTitle_ReturnsError verifies per-child validation.
func TestDecomposeDryRun_InvalidTitle_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		children []service.DecomposeInput
	}{
		{
			"empty title",
			[]service.DecomposeInput{{Title: ""}},
		},
		{
			"title too long",
			[]service.DecomposeInput{{Title: string(make([]byte, model.MaxTitleLength+1))}},
		},
		{
			"empty children",
			[]service.DecomposeInput{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.DecomposeDryRun(ctx, parent.ID, tt.children)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// TestDecomposeDryRun_IncludesPriority verifies priority is carried through.
func TestDecomposeDryRun_IncludesPriority(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	proposed, err := svc.DecomposeDryRun(ctx, parent.ID, []service.DecomposeInput{
		{Title: "Critical Task", Priority: model.PriorityCritical},
		{Title: "Low Task", Priority: model.PriorityLow},
	})
	require.NoError(t, err)
	require.Len(t, proposed, 2)
	assert.Equal(t, model.PriorityCritical, proposed[0].Priority)
	assert.Equal(t, model.PriorityLow, proposed[1].Priority)
}

// TestDecomposeDryRun_NodeCountUnchanged verifies no side effects.
func TestDecomposeDryRun_NodeCountUnchanged(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Get initial child count.
	before, err := s.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)

	// Run dry-run with multiple children.
	_, err = svc.DecomposeDryRun(ctx, parent.ID, []service.DecomposeInput{
		{Title: "A"}, {Title: "B"}, {Title: "C"},
	})
	require.NoError(t, err)

	// Verify count unchanged.
	after, err := s.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "dry-run must not change node count")
}
