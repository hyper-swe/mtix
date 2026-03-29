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

// newTestPromptService creates a PromptService backed by real SQLite for integration tests.
func newTestPromptService(t *testing.T) (
	*service.PromptService, *service.NodeService, *sqlite.Store, *recordingBroadcaster,
) {
	t.Helper()

	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{AutoClaimEnabled: false}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	logger := slog.Default()

	nodeSvc := service.NewNodeService(s, bc, cfg, logger, clock)
	promptSvc := service.NewPromptService(s, bc, logger, clock)

	return promptSvc, nodeSvc, s, bc
}

// TestUpdatePrompt_ChangesPromptField verifies prompt update.
func TestUpdatePrompt_ChangesPromptField(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Prompt Test", Prompt: "Original", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.UpdatePrompt(ctx, node.ID, "Updated prompt", "editor@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated prompt", got.Prompt)
}

// TestUpdatePrompt_RecomputesContentHash verifies hash update.
func TestUpdatePrompt_RecomputesContentHash(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Hash Test", Prompt: "Original", Creator: "admin",
	})
	require.NoError(t, err)

	// Get original hash.
	original, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	oldHash := original.ContentHash

	err = promptSvc.UpdatePrompt(ctx, node.ID, "Different prompt", "editor@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldHash, got.ContentHash)
	assert.NotEmpty(t, got.ContentHash)
}

// TestUpdatePrompt_CreatesActivityEntry verifies activity recording.
func TestUpdatePrompt_CreatesActivityEntry(t *testing.T) {
	promptSvc, nodeSvc, _, bc := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Activity Test", Prompt: "Original", Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()
	err = promptSvc.UpdatePrompt(ctx, node.ID, "Updated", "editor@test.com")
	require.NoError(t, err)

	events := bc.Events()
	require.GreaterOrEqual(t, len(events), 1)

	var found bool
	for _, e := range events {
		if e.Type == service.EventNodeUpdated {
			found = true
			break
		}
	}
	assert.True(t, found, "should broadcast node.updated event")
}

// TestAddAnnotation_GeneratesULID verifies ULID generation.
func TestAddAnnotation_GeneratesULID(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Annotation Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Review this section", "reviewer@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	assert.Len(t, got.Annotations[0].ID, 26, "ULID should be 26 characters")
	assert.Equal(t, "Review this section", got.Annotations[0].Text)
	assert.Equal(t, "reviewer@test.com", got.Annotations[0].Author)
	assert.False(t, got.Annotations[0].Resolved)
}

// TestAddAnnotation_AppendsToArray verifies annotations append.
func TestAddAnnotation_AppendsToArray(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Multi-Annotation", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "First annotation", "reviewer1@test.com")
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Second annotation", "reviewer2@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 2)
	assert.Equal(t, "First annotation", got.Annotations[0].Text)
	assert.Equal(t, "Second annotation", got.Annotations[1].Text)
}

// TestResolveAnnotation_SetsResolvedTrue verifies resolve.
func TestResolveAnnotation_SetsResolvedTrue(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Resolve Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Needs review", "reviewer@test.com")
	require.NoError(t, err)

	// Get annotation ID.
	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	annotID := got.Annotations[0].ID

	err = promptSvc.ResolveAnnotation(ctx, node.ID, annotID, true)
	require.NoError(t, err)

	got, err = s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.True(t, got.Annotations[0].Resolved)
}

// TestResolveAnnotation_UnresolveSetsResolvedFalse verifies unresolve.
func TestResolveAnnotation_UnresolveSetsResolvedFalse(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Unresolve Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Needs review", "reviewer@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	annotID := got.Annotations[0].ID

	// Resolve then unresolve.
	require.NoError(t, promptSvc.ResolveAnnotation(ctx, node.ID, annotID, true))
	require.NoError(t, promptSvc.ResolveAnnotation(ctx, node.ID, annotID, false))

	got, err = s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.False(t, got.Annotations[0].Resolved)
}

// TestResolveAnnotation_NonExistentID_ReturnsNotFound verifies error.
func TestResolveAnnotation_NonExistentID_ReturnsNotFound(t *testing.T) {
	promptSvc, nodeSvc, _, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Missing Annotation", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.ResolveAnnotation(ctx, node.ID, "NONEXISTENT", true)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUpdatePrompt_NonExistentNode_ReturnsError verifies error.
func TestUpdatePrompt_NonExistentNode_ReturnsError(t *testing.T) {
	promptSvc, _, _, _ := newTestPromptService(t)
	ctx := context.Background()

	err := promptSvc.UpdatePrompt(ctx, "NONEXISTENT", "text", "author")
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestAddAnnotation_NonExistentNode_ReturnsError verifies error.
func TestAddAnnotation_NonExistentNode_ReturnsError(t *testing.T) {
	promptSvc, _, _, _ := newTestPromptService(t)
	ctx := context.Background()

	err := promptSvc.AddAnnotation(ctx, "NONEXISTENT", "note", "author")
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestResolveAnnotation_NonExistentNode_ReturnsError verifies error.
func TestResolveAnnotation_NonExistentNode_ReturnsError(t *testing.T) {
	promptSvc, _, _, _ := newTestPromptService(t)
	ctx := context.Background()

	err := promptSvc.ResolveAnnotation(ctx, "NONEXISTENT", "ann-id", true)
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}
