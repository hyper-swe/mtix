// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package e2e contains end-to-end tests per MTIX-11.2.
// Tests use real SQLite databases with full service stack — no mocks.
// Each test creates an isolated temp database to prevent interference.
package e2e

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// testClock returns a fixed-time clock for deterministic E2E tests.
func testClock() func() time.Time {
	fixed := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}

// e2eEnv holds all services needed for E2E testing.
type e2eEnv struct {
	store      store.Store
	sqlStore   *sqlite.Store
	nodeSvc    *service.NodeService
	sessionSvc *service.SessionService
	agentSvc   *service.AgentService
	ctx        context.Context
}

// setupE2E creates a full E2E environment with real SQLite store and all services.
func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}
	clock := testClock()

	nodeSvc := service.NewNodeService(st, broadcaster, config, logger, clock)
	sessionSvc := service.NewSessionService(st, config, logger, clock)
	agentSvc := service.NewAgentService(st, broadcaster, config, logger, clock)

	return &e2eEnv{
		store:      st,
		sqlStore:   st,
		nodeSvc:    nodeSvc,
		sessionSvc: sessionSvc,
		agentSvc:   agentSvc,
		ctx:        context.Background(),
	}
}

// ensureAgent creates an agent record in the agents table if it doesn't exist.
// This is required because sessions table has FK constraint to agents(agent_id).
func ensureAgent(t *testing.T, env *e2eEnv, agentID, project string) {
	t.Helper()
	db := env.store.WriteDB()
	_, err := db.ExecContext(env.ctx,
		`INSERT OR IGNORE INTO agents (agent_id, project, state) VALUES (?, ?, 'idle')`,
		agentID, project,
	)
	require.NoError(t, err)
}

// TestE2E_AgentWorkflow_CreateStory verifies an agent can create a top-level story.
func TestE2E_AgentWorkflow_CreateStory(t *testing.T) {
	env := setupE2E(t)

	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "User Authentication",
		Project: "AUTH",
		Creator: "agent-001",
	})
	require.NoError(t, err)
	require.NotEmpty(t, story.ID)

	assert.Equal(t, "User Authentication", story.Title)
	assert.Equal(t, model.StatusOpen, story.Status)
	assert.Equal(t, "AUTH", story.Project)
	assert.Equal(t, 0, story.Depth)

	// Verify persisted via independent read.
	fetched, err := env.nodeSvc.GetNode(env.ctx, story.ID)
	require.NoError(t, err)
	assert.Equal(t, story.ID, fetched.ID)
	assert.Equal(t, story.Title, fetched.Title)
}

// TestE2E_AgentWorkflow_DecomposeIntoEpics verifies decomposition into epics.
func TestE2E_AgentWorkflow_DecomposeIntoEpics(t *testing.T) {
	env := setupE2E(t)

	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "User Authentication",
		Project: "AUTH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Login Flow"},
		{Title: "Registration Flow"},
		{Title: "Password Reset"},
	}, "agent-001")
	require.NoError(t, err)
	assert.Len(t, epicIDs, 3)

	// Verify each epic is a child of the story.
	for _, epicID := range epicIDs {
		epic, err := env.nodeSvc.GetNode(env.ctx, epicID)
		require.NoError(t, err)
		assert.Equal(t, story.ID, epic.ParentID)
		assert.Equal(t, 1, epic.Depth)
		assert.Equal(t, model.StatusOpen, epic.Status)
	}
}

// TestE2E_AgentWorkflow_ClaimEpic verifies an agent can claim an epic.
func TestE2E_AgentWorkflow_ClaimEpic(t *testing.T) {
	env := setupE2E(t)

	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "User Auth",
		Project: "AUTH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Login Flow"},
	}, "agent-001")
	require.NoError(t, err)
	require.Len(t, epicIDs, 1)

	// Claim the epic.
	err = env.store.ClaimNode(env.ctx, epicIDs[0], "agent-001")
	require.NoError(t, err)

	// Verify claimed state.
	epic, err := env.nodeSvc.GetNode(env.ctx, epicIDs[0])
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, epic.Status)
	assert.Equal(t, "agent-001", epic.Assignee)
}

// TestE2E_AgentWorkflow_DecomposeIntoIssues verifies decomposition of epic into issues.
func TestE2E_AgentWorkflow_DecomposeIntoIssues(t *testing.T) {
	env := setupE2E(t)

	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "User Auth",
		Project: "AUTH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Login Flow"},
	}, "agent-001")
	require.NoError(t, err)

	// Decompose the epic into 5 issues.
	issueIDs, err := env.nodeSvc.Decompose(env.ctx, epicIDs[0], []service.DecomposeInput{
		{Title: "Design login form"},
		{Title: "Implement email validation"},
		{Title: "Add password hashing"},
		{Title: "Create JWT tokens"},
		{Title: "Write login tests"},
	}, "agent-001")
	require.NoError(t, err)
	assert.Len(t, issueIDs, 5)

	// Verify hierarchy: each issue has the epic as parent.
	for _, issueID := range issueIDs {
		issue, err := env.nodeSvc.GetNode(env.ctx, issueID)
		require.NoError(t, err)
		assert.Equal(t, epicIDs[0], issue.ParentID)
		assert.Equal(t, 2, issue.Depth)
		assert.Equal(t, model.StatusOpen, issue.Status)
	}
}

// TestE2E_AgentWorkflow_CompleteAllIssues verifies claim→done for each issue.
func TestE2E_AgentWorkflow_CompleteAllIssues(t *testing.T) {
	env := setupE2E(t)

	// Build hierarchy: story → epic → 5 issues.
	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "User Auth",
		Project: "AUTH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Login Flow"},
	}, "agent-001")
	require.NoError(t, err)

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, epicIDs[0], []service.DecomposeInput{
		{Title: "Issue 1"},
		{Title: "Issue 2"},
		{Title: "Issue 3"},
		{Title: "Issue 4"},
		{Title: "Issue 5"},
	}, "agent-001")
	require.NoError(t, err)

	// Agent claims and completes each issue.
	for _, issueID := range issueIDs {
		err = env.store.ClaimNode(env.ctx, issueID, "agent-001")
		require.NoError(t, err, "claim %s", issueID)

		err = env.nodeSvc.TransitionStatus(env.ctx, issueID, model.StatusDone,
			"completed", "agent-001")
		require.NoError(t, err, "done %s", issueID)
	}

	// Verify all issues are done.
	for _, issueID := range issueIDs {
		issue, err := env.nodeSvc.GetNode(env.ctx, issueID)
		require.NoError(t, err)
		assert.Equal(t, model.StatusDone, issue.Status)
	}
}

// TestE2E_AgentWorkflow_ProgressRollup100 verifies progress rolls up to 100%.
func TestE2E_AgentWorkflow_ProgressRollup100(t *testing.T) {
	env := setupE2E(t)

	// Build hierarchy: story → epic → 3 issues.
	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "User Auth",
		Project: "AUTH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Login Flow"},
	}, "agent-001")
	require.NoError(t, err)

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, epicIDs[0], []service.DecomposeInput{
		{Title: "Issue 1"},
		{Title: "Issue 2"},
		{Title: "Issue 3"},
	}, "agent-001")
	require.NoError(t, err)

	// Complete each issue, checking progress along the way.
	for i, issueID := range issueIDs {
		err = env.store.ClaimNode(env.ctx, issueID, "agent-001")
		require.NoError(t, err)
		err = env.nodeSvc.TransitionStatus(env.ctx, issueID, model.StatusDone,
			"done", "agent-001")
		require.NoError(t, err)

		// Check epic progress after each completion.
		epic, err := env.store.GetNode(env.ctx, epicIDs[0])
		require.NoError(t, err)
		expectedProgress := float64(i+1) / float64(len(issueIDs))
		assert.InDelta(t, expectedProgress, epic.Progress, 0.01,
			"epic progress after %d/%d issues done", i+1, len(issueIDs))
	}

	// Verify story-level progress = 100%.
	storyNode, err := env.store.GetNode(env.ctx, story.ID)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, storyNode.Progress, 0.01,
		"story progress should be 100%% after all issues done")
}

// TestE2E_AgentWorkflow_ActivityStream verifies activity is recorded.
func TestE2E_AgentWorkflow_ActivityStream(t *testing.T) {
	env := setupE2E(t)

	// Create a story and a child to generate activity.
	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Activity Test",
		Project: "ACT",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	childIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Task 1"},
	}, "agent-001")
	require.NoError(t, err)

	// Claim and complete.
	err = env.store.ClaimNode(env.ctx, childIDs[0], "agent-001")
	require.NoError(t, err)
	err = env.nodeSvc.TransitionStatus(env.ctx, childIDs[0], model.StatusDone,
		"completed", "agent-001")
	require.NoError(t, err)

	// Verify activity records exist by querying the activity JSON column in nodes.
	// Activity is stored as a JSON array TEXT column in the nodes table (FR-3.6).
	var activityJSON string
	err = env.store.QueryRow(env.ctx,
		"SELECT activity FROM nodes WHERE id = ? AND deleted_at IS NULL", childIDs[0],
	).Scan(&activityJSON)
	require.NoError(t, err)
	assert.NotEqual(t, "[]", activityJSON,
		"activity stream should record actions for the completed node")
	assert.Greater(t, len(activityJSON), 2,
		"activity JSON should contain entries")
}

// TestE2E_AgentWorkflow_FTSSearch verifies full-text search finds created nodes.
func TestE2E_AgentWorkflow_FTSSearch(t *testing.T) {
	env := setupE2E(t)

	// Create nodes with searchable titles.
	_, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Implement OAuth2 Authentication",
		Project: "SRCH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	_, err = env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Database Migration Scripts",
		Project: "SRCH",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	// Search for "OAuth2" — should find the first node.
	// FTS5 tokenizes "OAuth2" as a single token; searching for "OAuth" won't match.
	results, total, err := env.store.SearchNodes(env.ctx, "OAuth2",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, 1, "FTS should find node containing 'OAuth2'")
	assert.GreaterOrEqual(t, len(results), 1)

	found := false
	for _, r := range results {
		if r.Title == "Implement OAuth2 Authentication" {
			found = true
			break
		}
	}
	assert.True(t, found, "FTS results should contain the OAuth2 node")

	// Search for "Migration" — should find the second node.
	_, migTotal, migErr := env.store.SearchNodes(env.ctx, "Migration",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, migErr)
	assert.GreaterOrEqual(t, migTotal, 1, "FTS should find node containing 'Migration'")
}

// TestE2E_AgentWorkflow_TreeStructure verifies the full tree structure.
func TestE2E_AgentWorkflow_TreeStructure(t *testing.T) {
	env := setupE2E(t)

	// Build: story → 3 epics → 2 issues each = 1 + 3 + 6 = 10 nodes.
	story, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Full Tree",
		Project: "TREE",
		Creator: "agent-001",
	})
	require.NoError(t, err)

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, story.ID, []service.DecomposeInput{
		{Title: "Epic A"},
		{Title: "Epic B"},
		{Title: "Epic C"},
	}, "agent-001")
	require.NoError(t, err)
	assert.Len(t, epicIDs, 3)

	totalIssues := 0
	for _, epicID := range epicIDs {
		issueIDs, err := env.nodeSvc.Decompose(env.ctx, epicID, []service.DecomposeInput{
			{Title: "Issue 1"},
			{Title: "Issue 2"},
		}, "agent-001")
		require.NoError(t, err)
		assert.Len(t, issueIDs, 2)
		totalIssues += len(issueIDs)
	}
	assert.Equal(t, 6, totalIssues)

	// Retrieve full tree.
	tree, err := env.store.GetTree(env.ctx, story.ID, 10)
	require.NoError(t, err)

	// 1 story + 3 epics + 6 issues = 10 nodes total.
	assert.Len(t, tree, 10, "tree should contain 10 nodes (1 story + 3 epics + 6 issues)")

	// Verify depth distribution.
	depthCounts := map[int]int{}
	for _, node := range tree {
		depthCounts[node.Depth]++
	}
	assert.Equal(t, 1, depthCounts[0], "1 story at depth 0")
	assert.Equal(t, 3, depthCounts[1], "3 epics at depth 1")
	assert.Equal(t, 6, depthCounts[2], "6 issues at depth 2")

	// Verify all nodes have correct project.
	for _, node := range tree {
		assert.Equal(t, "TREE", node.Project)
	}
}
