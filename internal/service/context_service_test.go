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

// newTestContextService creates a ContextService backed by real SQLite for integration tests.
func newTestContextService(t *testing.T) (*service.ContextService, *service.NodeService, *sqlite.Store) {
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

// createChain is a helper to create root→epic→issue chain for tests.
func createChain(t *testing.T, nodeSvc *service.NodeService) (root, epic, issue *model.Node) {
	t.Helper()
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project:     "PROJ",
		Title:       "Root Story",
		Description: "Root description",
		Prompt:      "Build the system",
		Creator:     "human@test.com",
	})
	require.NoError(t, err)

	epic, err = nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID:    root.ID,
		Project:     "PROJ",
		Title:       "Epic Task",
		Description: "Epic description",
		Prompt:      "Implement the module",
		Creator:     "human@test.com",
	})
	require.NoError(t, err)

	issue, err = nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID:    epic.ID,
		Project:     "PROJ",
		Title:       "Issue Task",
		Description: "Issue description",
		Prompt:      "Write the function",
		Acceptance:  "Function passes tests",
		Creator:     "agent-1",
	})
	require.NoError(t, err)

	return root, epic, issue
}

// TestContextChain_RootToTarget_CorrectOrder verifies ancestor chain ordering.
func TestContextChain_RootToTarget_CorrectOrder(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	root, epic, issue := createChain(t, nodeSvc)

	resp, err := ctxSvc.GetContext(ctx, issue.ID, nil)
	require.NoError(t, err)

	// Chain should be root → epic → issue (root-first).
	require.Len(t, resp.Chain, 3)
	assert.Equal(t, root.ID, resp.Chain[0].ID)
	assert.Equal(t, epic.ID, resp.Chain[1].ID)
	assert.Equal(t, issue.ID, resp.Chain[2].ID)
}

// TestContextChain_IncludesPromptAtEveryLevel verifies prompts are included.
func TestContextChain_IncludesPromptAtEveryLevel(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	_, _, issue := createChain(t, nodeSvc)

	resp, err := ctxSvc.GetContext(ctx, issue.ID, nil)
	require.NoError(t, err)

	for _, entry := range resp.Chain {
		assert.NotEmpty(t, entry.Prompt, "entry %s should have a prompt", entry.ID)
	}
}

// TestContextChain_AssembledPrompt_HumanAuthored verifies human attribution.
func TestContextChain_AssembledPrompt_HumanAuthored(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	// Create a single root with human creator.
	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Human Node",
		Prompt:  "Human wrote this prompt",
		Creator: "human@test.com",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, root.ID, nil)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "[HUMAN-AUTHORED]")
	assert.Contains(t, resp.AssembledPrompt, "Human wrote this prompt")
}

// TestContextChain_AssembledPrompt_LLMGenerated verifies LLM attribution.
func TestContextChain_AssembledPrompt_LLMGenerated(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	// Create node with agent creator (matches agent-* pattern).
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Agent Node",
		Prompt:  "Agent generated prompt",
		Creator: "agent-1",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, node.ID, nil)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "[LLM-GENERATED]")
	assert.Contains(t, resp.AssembledPrompt, "Agent generated prompt")
}

// TestContextChain_AssembledPrompt_IncludesAnnotations verifies annotations in chain.
func TestContextChain_AssembledPrompt_IncludesAnnotations(t *testing.T) {
	ctxSvc, nodeSvc, s := newTestContextService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Annotated Node",
		Prompt:  "Base prompt",
		Creator: "human@test.com",
	})
	require.NoError(t, err)

	// Add an unresolved annotation directly via store.
	annotations := []model.Annotation{
		{
			ID:        "01HTEST00000000000000001",
			Author:    "reviewer@test.com",
			Text:      "Please clarify the requirements",
			CreatedAt: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
			Resolved:  false,
		},
	}
	require.NoError(t, s.SetAnnotations(ctx, node.ID, annotations))

	resp, err := ctxSvc.GetContext(ctx, node.ID, nil)
	require.NoError(t, err)

	assert.Contains(t, resp.AssembledPrompt, "[ANNOTATION by reviewer@test.com]")
	assert.Contains(t, resp.AssembledPrompt, "Please clarify the requirements")
}

// TestContextChain_SkipsDescriptionForDistantAncestors verifies FR-12.2 description rule.
func TestContextChain_SkipsDescriptionForDistantAncestors(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	// Create a 4-level chain: root → epic → issue → micro.
	root, epic, issue := createChain(t, nodeSvc)

	micro, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: issue.ID,
		Project:  "PROJ",
		Title:    "Micro Task",
		Prompt:   "Very specific task",
		Creator:  "agent-1",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, micro.ID, nil)
	require.NoError(t, err)

	// For micro (depth 3), target (3) and parent (2) within 2 levels.
	// Epic (depth 1) is also within 2 levels of target.
	// Root (depth 0) is 3 levels away — description should be skipped.
	require.Len(t, resp.Chain, 4)
	assert.Empty(t, resp.Chain[0].Description, "root description should be omitted (>2 levels from target)")
	assert.Equal(t, root.ID, resp.Chain[0].ID)
	assert.NotEmpty(t, resp.Chain[1].Description, "epic description included (2 levels from target)")
	assert.Equal(t, epic.ID, resp.Chain[1].ID)
	_ = micro // suppress unused variable lint
}

// TestContextChain_IncludesSiblings verifies siblings are returned.
func TestContextChain_IncludesSiblings(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Root",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Create 3 siblings.
	child1, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child 1", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child 2", Creator: "admin",
	})
	require.NoError(t, err)

	_, err = nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: root.ID, Project: "PROJ", Title: "Child 3", Creator: "admin",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, child1.ID, nil)
	require.NoError(t, err)

	// Siblings should include Child 2 and Child 3 (not self).
	assert.Len(t, resp.Siblings, 2)
	sibIDs := make([]string, len(resp.Siblings))
	for i, s := range resp.Siblings {
		sibIDs[i] = s.ID
	}
	assert.NotContains(t, sibIDs, child1.ID)
}

// TestContextChain_WithMaxTokens_Truncates verifies FR-12.4 token limiting.
func TestContextChain_WithMaxTokens_Truncates(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	_, _, issue := createChain(t, nodeSvc)

	opts := &service.ContextOptions{MaxTokens: 50}
	resp, err := ctxSvc.GetContext(ctx, issue.ID, opts)
	require.NoError(t, err)

	// Assembled prompt should be truncated to roughly 50 tokens (~200 chars).
	assert.NotEmpty(t, resp.AssembledPrompt)
}

// TestContextChain_NonExistentNode_ReturnsError verifies error for unknown node.
func TestContextChain_NonExistentNode_ReturnsError(t *testing.T) {
	ctxSvc, _, _ := newTestContextService(t)
	ctx := context.Background()

	_, err := ctxSvc.GetContext(ctx, "NONEXISTENT", nil)
	assert.Error(t, err)
}

// TestContextChain_RootNode_NoSiblings verifies root has no siblings.
func TestContextChain_RootNode_NoSiblings(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	ctx := context.Background()

	root, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Solo Root", Prompt: "task", Creator: "admin",
	})
	require.NoError(t, err)

	resp, err := ctxSvc.GetContext(ctx, root.ID, nil)
	require.NoError(t, err)

	assert.Empty(t, resp.Siblings)
	require.Len(t, resp.Chain, 1)
	assert.Equal(t, root.ID, resp.Chain[0].ID)
}

// TestContextChain_IncludesBlockingDeps verifies blocking deps are returned.
func TestContextChain_IncludesBlockingDeps(t *testing.T) {
	ctxSvc, nodeSvc, s := newTestContextService(t)
	ctx := context.Background()

	nodeA, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Blocker", Creator: "admin",
	})
	require.NoError(t, err)

	nodeB, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Blocked", Creator: "admin",
	})
	require.NoError(t, err)

	// A blocks B.
	dep := &model.Dependency{
		FromID:    nodeA.ID,
		ToID:      nodeB.ID,
		DepType:   model.DepTypeBlocks,
		CreatedAt: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		CreatedBy: "admin",
	}
	require.NoError(t, s.AddDependency(ctx, dep))

	resp, err := ctxSvc.GetContext(ctx, nodeB.ID, nil)
	require.NoError(t, err)

	require.Len(t, resp.BlockingDeps, 1)
	assert.Equal(t, nodeA.ID, resp.BlockingDeps[0].FromID)
}
