// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestE2E_ConcurrentAgents_NoDoubleClaim verifies concurrent claim attempts
// result in exactly one winner per node.
func TestE2E_ConcurrentAgents_NoDoubleClaim(t *testing.T) {
	env := setupE2E(t)

	// Create a parent with 10 issues.
	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Concurrent Parent",
		Project: "CONC",
		Creator: "admin",
	})
	require.NoError(t, err)

	inputs := make([]service.DecomposeInput, 10)
	for i := range inputs {
		inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Issue %d", i+1)}
	}

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, inputs, "admin")
	require.NoError(t, err)
	require.Len(t, issueIDs, 10)

	// 3 agents compete to claim all 10 issues concurrently.
	var wg sync.WaitGroup
	claimResults := sync.Map{} // map[issueID] → agentID that won
	var claimErrors int64

	for agentNum := 1; agentNum <= 3; agentNum++ {
		agentID := fmt.Sprintf("agent-%03d", agentNum)
		wg.Add(1)
		go func(aid string) {
			defer wg.Done()
			for _, issueID := range issueIDs {
				err := env.store.ClaimNode(env.ctx, issueID, aid)
				if err == nil {
					claimResults.Store(issueID, aid)
				} else {
					atomic.AddInt64(&claimErrors, 1)
				}
			}
		}(agentID)
	}
	wg.Wait()

	// Each issue should be claimed by exactly one agent.
	claimed := 0
	claimResults.Range(func(_, _ any) bool {
		claimed++
		return true
	})
	assert.Equal(t, 10, claimed, "all 10 issues should be claimed")

	// Total claims (10 successes) + errors (20 failures) = 30 total attempts.
	totalErrors := atomic.LoadInt64(&claimErrors)
	assert.Equal(t, int64(20), totalErrors,
		"20 claim attempts should fail (3 agents × 10 issues − 10 successful claims)")

	_ = parent
}

// TestE2E_ConcurrentAgents_ProgressRollupCorrect verifies progress is correct
// after concurrent completions.
func TestE2E_ConcurrentAgents_ProgressRollupCorrect(t *testing.T) {
	env := setupE2E(t)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Progress Parent",
		Project: "PROG",
		Creator: "admin",
	})
	require.NoError(t, err)

	inputs := make([]service.DecomposeInput, 5)
	for i := range inputs {
		inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Task %d", i+1)}
	}

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, inputs, "admin")
	require.NoError(t, err)

	// Pre-claim all issues to different agents.
	for i, issueID := range issueIDs {
		agentID := fmt.Sprintf("agent-%03d", (i%3)+1)
		err = env.store.ClaimNode(env.ctx, issueID, agentID)
		require.NoError(t, err)
	}

	// Complete all issues concurrently.
	var wg sync.WaitGroup
	for _, issueID := range issueIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			err := env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
				"completed", "agent")
			assert.NoError(t, err, "completing %s", id)
		}(issueID)
	}
	wg.Wait()

	// Verify parent progress is 100%.
	parentNode, err := env.store.GetNode(env.ctx, parent.ID)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, parentNode.Progress, 0.01,
		"parent progress should be 100%% after all concurrent completions")
}

// TestE2E_ConcurrentAgents_NoDataCorruption verifies no data corruption
// under concurrent access by running mtix verify.
func TestE2E_ConcurrentAgents_NoDataCorruption(t *testing.T) {
	env := setupE2E(t)

	// Create a reasonable tree.
	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Corruption Check",
		Project: "CORR",
		Creator: "admin",
	})
	require.NoError(t, err)

	inputs := make([]service.DecomposeInput, 10)
	for i := range inputs {
		inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Node %d", i+1)}
	}

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, inputs, "admin")
	require.NoError(t, err)

	// Concurrent operations: claim, update, transition.
	var wg sync.WaitGroup
	for i, issueID := range issueIDs {
		agentID := fmt.Sprintf("agent-%03d", (i%3)+1)
		wg.Add(1)
		go func(id, agent string) {
			defer wg.Done()
			_ = env.store.ClaimNode(env.ctx, id, agent)
			newTitle := fmt.Sprintf("Updated by %s", agent)
			_ = env.store.UpdateNode(env.ctx, id, &store.NodeUpdate{Title: &newTitle})
			_ = env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
				"done", agent)
		}(issueID, agentID)
	}
	wg.Wait()

	// Run verify — should pass with no corruption.
	result, err := env.sqlStore.Verify(env.ctx)
	require.NoError(t, err)
	assert.True(t, result.IntegrityOK, "SQLite integrity should be OK")
	assert.True(t, result.ForeignKeyOK, "foreign keys should be OK")
	assert.True(t, result.FTSOK, "FTS should be OK")
}

// TestE2E_ConcurrentAgents_ActivityStreamAccurate verifies activity records
// are accurate after concurrent operations.
func TestE2E_ConcurrentAgents_ActivityStreamAccurate(t *testing.T) {
	env := setupE2E(t)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Activity Accuracy",
		Project: "ACTA",
		Creator: "admin",
	})
	require.NoError(t, err)

	inputs := make([]service.DecomposeInput, 5)
	for i := range inputs {
		inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Act %d", i+1)}
	}

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, inputs, "admin")
	require.NoError(t, err)

	// Each agent claims and completes their assigned issues.
	var wg sync.WaitGroup
	for i, issueID := range issueIDs {
		agentID := fmt.Sprintf("agent-%03d", i+1)
		wg.Add(1)
		go func(id, agent string) {
			defer wg.Done()
			_ = env.store.ClaimNode(env.ctx, id, agent)
			_ = env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
				"done", agent)
		}(issueID, agentID)
	}
	wg.Wait()

	// Verify activity records exist for each issue.
	// Activity is stored as a JSON array TEXT column in the nodes table (FR-3.6).
	for _, issueID := range issueIDs {
		var activityJSON string
		err := env.store.QueryRow(env.ctx,
			"SELECT activity FROM nodes WHERE id = ? AND deleted_at IS NULL", issueID,
		).Scan(&activityJSON)
		require.NoError(t, err)
		assert.NotEqual(t, "[]", activityJSON,
			"issue %s should have activity records", issueID)
	}
}

// TestE2E_ConcurrentAgents_AllIssuesCompleted verifies all issues reach done state.
func TestE2E_ConcurrentAgents_AllIssuesCompleted(t *testing.T) {
	env := setupE2E(t)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "All Done",
		Project: "DONE",
		Creator: "admin",
	})
	require.NoError(t, err)

	inputs := make([]service.DecomposeInput, 10)
	for i := range inputs {
		inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Issue %d", i+1)}
	}

	issueIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, inputs, "admin")
	require.NoError(t, err)

	// Pre-assign each issue to an agent, then complete concurrently.
	for i, issueID := range issueIDs {
		agentID := fmt.Sprintf("agent-%03d", (i%3)+1)
		err = env.store.ClaimNode(env.ctx, issueID, agentID)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	for i, issueID := range issueIDs {
		agentID := fmt.Sprintf("agent-%03d", (i%3)+1)
		wg.Add(1)
		go func(id, agent string) {
			defer wg.Done()
			_ = env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
				"completed", agent)
		}(issueID, agentID)
	}
	wg.Wait()

	// Verify all issues are done.
	for _, issueID := range issueIDs {
		issue, err := env.nodeSvc.GetNode(env.ctx, issueID)
		require.NoError(t, err)
		assert.Equal(t, model.StatusDone, issue.Status,
			"issue %s should be done", issueID)
	}
}

// TestE2E_ConcurrentAgents_SessionsIndependent verifies concurrent sessions.
func TestE2E_ConcurrentAgents_SessionsIndependent(t *testing.T) {
	env := setupE2E(t)

	var wg sync.WaitGroup
	sessionIDs := sync.Map{}

	// Create agent records for FK constraint.
	for i := 1; i <= 3; i++ {
		ensureAgent(t, env, fmt.Sprintf("agent-%03d", i), "SESS")
	}

	// Start 3 concurrent sessions.
	for i := 1; i <= 3; i++ {
		agentID := fmt.Sprintf("agent-%03d", i)
		wg.Add(1)
		go func(aid string) {
			defer wg.Done()
			sid, err := env.sessionSvc.SessionStart(env.ctx, aid, "SESS")
			assert.NoError(t, err)
			sessionIDs.Store(aid, sid)
		}(agentID)
	}
	wg.Wait()

	// Verify all sessions are distinct and active.
	seen := map[string]bool{}
	sessionIDs.Range(func(key, value any) bool {
		sid := value.(string)
		assert.False(t, seen[sid], "session ID %s should be unique", sid)
		seen[sid] = true
		return true
	})
	assert.Len(t, seen, 3, "should have 3 distinct sessions")
}

// TestE2E_ConcurrentAgents_VerifyPasses verifies mtix verify passes after
// concurrent agent operations.
func TestE2E_ConcurrentAgents_VerifyPasses(t *testing.T) {
	env := setupE2E(t)

	// Build a reasonably complex tree.
	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Verify After Concurrent",
		Project: "VERI",
		Creator: "admin",
	})
	require.NoError(t, err)

	epicInputs := make([]service.DecomposeInput, 3)
	for i := range epicInputs {
		epicInputs[i] = service.DecomposeInput{
			Title: fmt.Sprintf("Epic %d", i+1),
		}
	}

	epicIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, epicInputs, "admin")
	require.NoError(t, err)

	// Decompose each epic into issues concurrently.
	var wg sync.WaitGroup
	allIssueIDs := sync.Map{}

	for _, epicID := range epicIDs {
		wg.Add(1)
		go func(eid string) {
			defer wg.Done()
			issueInputs := make([]service.DecomposeInput, 3)
			for i := range issueInputs {
				issueInputs[i] = service.DecomposeInput{
					Title: fmt.Sprintf("Issue under %s #%d", eid, i+1),
				}
			}
			ids, err := env.nodeSvc.Decompose(context.Background(), eid, issueInputs, "admin")
			assert.NoError(t, err)
			for _, id := range ids {
				allIssueIDs.Store(id, true)
			}
		}(epicID)
	}
	wg.Wait()

	// Claim and complete all issues concurrently.
	allIssueIDs.Range(func(key, _ any) bool {
		issueID := key.(string)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = env.store.ClaimNode(env.ctx, id, "agent-001")
			_ = env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
				"done", "agent-001")
		}(issueID)
		return true
	})
	wg.Wait()

	// Verify passes.
	result, err := env.sqlStore.Verify(env.ctx)
	require.NoError(t, err)
	assert.True(t, result.AllPassed,
		"verify should pass after concurrent operations, errors: %v", result.Errors)
}
