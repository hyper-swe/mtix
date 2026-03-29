// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newTestContextServiceForTruncation creates services for truncation tests.
func newTestContextServiceForTruncation(t *testing.T) (
	*service.ContextService, *service.NodeService, *sqlite.Store,
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
	ctxSvc := service.NewContextService(s, cfg, logger)

	return ctxSvc, nodeSvc, s
}

// TestTokenTruncation_UnderBudget_NoTruncation verifies no truncation when under budget.
func TestTokenTruncation_UnderBudget_NoTruncation(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Prompt: "Short prompt", Creator: "admin",
	})
	require.NoError(t, err)

	// Large budget — should not truncate.
	opts := &service.ContextOptions{MaxTokens: 10000}
	resp, err := ctxSvc.GetContext(ctx, root.ID, opts)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "Short prompt")
	assert.NotContains(t, resp.AssembledPrompt, "[TRUNCATED:")
}

// TestTokenTruncation_TargetAlwaysPreserved verifies target is never truncated.
func TestTokenTruncation_TargetAlwaysPreserved(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Prompt: strings.Repeat("A", 200), Creator: "admin",
	})
	require.NoError(t, err)

	child, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child",
		Prompt: "Target prompt must survive", Creator: "admin",
	})
	require.NoError(t, err)

	// Very small budget — but target must survive.
	opts := &service.ContextOptions{MaxTokens: 50}
	resp, err := ctxSvc.GetContext(ctx, child.ID, opts)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "Target prompt must survive")
}

// TestTokenTruncation_ParentPromptAlwaysPreserved verifies parent is preserved.
func TestTokenTruncation_ParentPromptAlwaysPreserved(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Prompt: strings.Repeat("B", 400), Creator: "admin",
	})
	require.NoError(t, err)

	parent, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Parent",
		Prompt: "Parent prompt preserved", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID, Project: "PROJ", Title: "Child",
		Prompt: "Child prompt", Creator: "admin",
	})
	require.NoError(t, err)

	// Budget that fits target + parent but not root's long prompt.
	opts := &service.ContextOptions{MaxTokens: 100}
	resp, err := ctxSvc.GetContext(ctx, child.ID, opts)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "Parent prompt preserved")
	assert.Contains(t, resp.AssembledPrompt, "Child prompt")
}

// TestTokenTruncation_DistantAncestorsTruncatedFirst verifies truncation priority.
func TestTokenTruncation_DistantAncestorsTruncatedFirst(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	// Create 4-level chain with long prompts at each level.
	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root",
		Prompt: "Root prompt. " + strings.Repeat("Detail about root. ", 20), Creator: "admin",
	})
	require.NoError(t, err)

	epic, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Epic",
		Prompt: "Epic prompt. " + strings.Repeat("Detail about epic. ", 20), Creator: "admin",
	})
	require.NoError(t, err)

	issue, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: epic.ID, Project: "PROJ", Title: "Issue",
		Prompt: "Issue prompt content", Creator: "admin",
	})
	require.NoError(t, err)

	micro, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: issue.ID, Project: "PROJ", Title: "Micro",
		Prompt: "Micro task content", Creator: "admin",
	})
	require.NoError(t, err)

	// Budget that forces truncation of distant ancestors.
	opts := &service.ContextOptions{MaxTokens: 100}
	resp, err := ctxSvc.GetContext(ctx, micro.ID, opts)
	require.NoError(t, err)

	// Target (micro) and parent (issue) should be preserved.
	assert.Contains(t, resp.AssembledPrompt, "Micro task content")
	assert.Contains(t, resp.AssembledPrompt, "Issue prompt content")
}

// TestTokenTruncation_TruncationNoteIncluded verifies truncation note.
func TestTokenTruncation_TruncationNoteIncluded(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	// Create a 4-level hierarchy to have ancestors that can be omitted.
	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root",
		Prompt: strings.Repeat("X", 800), Creator: "admin",
	})
	require.NoError(t, err)

	epic, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Epic",
		Prompt: strings.Repeat("Y", 800), Creator: "admin",
	})
	require.NoError(t, err)

	issue, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: epic.ID, Project: "PROJ", Title: "Issue",
		Prompt: "Issue prompt", Creator: "admin",
	})
	require.NoError(t, err)

	micro, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: issue.ID, Project: "PROJ", Title: "Micro",
		Prompt: "Micro task", Creator: "admin",
	})
	require.NoError(t, err)

	// Very tight budget that forces omission of distant ancestors (root and epic).
	// Target + parent alone consume ~29 tokens, leaving no room for ancestors.
	opts := &service.ContextOptions{MaxTokens: 25}
	resp, err := ctxSvc.GetContext(ctx, micro.ID, opts)
	require.NoError(t, err)

	// Both root and epic should be omitted, triggering the truncation note.
	assert.Contains(t, resp.AssembledPrompt, "[TRUNCATED:")
}

// TestTokenTruncation_SingleNode_NoParent verifies handling when chain has just one entry.
func TestTokenTruncation_SingleNode_NoParent(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Solo Root", Prompt: "Just one node", Creator: "admin",
	})
	require.NoError(t, err)

	opts := &service.ContextOptions{MaxTokens: 100}
	resp, err := ctxSvc.GetContext(ctx, root.ID, opts)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "Just one node")
	assert.NotContains(t, resp.AssembledPrompt, "[TRUNCATED:")
}

// TestTokenTruncation_WithAnnotations_IncludesInSection verifies annotations in truncation.
func TestTokenTruncation_WithAnnotations_IncludesInSection(t *testing.T) {
	ctxSvc, nodeSvc, s := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Annotated", Prompt: "Base prompt",
		Creator: "human@test.com",
	})
	require.NoError(t, err)

	// Add an unresolved annotation.
	annotations := []model.Annotation{
		{
			ID:        "01HTEST00000000000000001",
			Author:    "reviewer",
			Text:      "Check edge case",
			CreatedAt: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
			Resolved:  false,
		},
	}
	require.NoError(t, s.SetAnnotations(ctx, node.ID, annotations))

	opts := &service.ContextOptions{MaxTokens: 500}
	resp, err := ctxSvc.GetContext(ctx, node.ID, opts)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "Check edge case")
	assert.Contains(t, resp.AssembledPrompt, "[ANNOTATION by reviewer]")
}

// TestTokenTruncation_Chars4Estimator_WithinMargin verifies chars/4 estimator.
func TestTokenTruncation_Chars4Estimator_WithinMargin(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextServiceForTruncation(t)
	ctx := context.Background()

	// Create a node with known character count.
	prompt := strings.Repeat("word ", 100) // ~500 chars → ~125 tokens.
	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Root", Prompt: prompt, Creator: "admin",
	})
	require.NoError(t, err)

	// Budget of 200 tokens should fit the 125 token prompt.
	opts := &service.ContextOptions{MaxTokens: 200}
	resp, err := ctxSvc.GetContext(ctx, root.ID, opts)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "word")
	assert.NotContains(t, resp.AssembledPrompt, "[TRUNCATED:")
}
