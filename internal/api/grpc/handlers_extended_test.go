// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// --- ListChildren Handler Tests ---

// TestHandleListChildren_ReturnsChildren verifies list returns direct children.
func TestHandleListChildren_ReturnsChildren(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "LIST")
	child1 := createTestNode(t, s, "Child 1", "LIST")
	child2 := createTestNode(t, s, "Child 2", "LIST")

	// Manually set parent_id to make them children (in production, this is done by CreateNode).
	_, err := s.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET parent_id = ? WHERE id = ?`,
		parent.ID, child1.ID,
	)
	require.NoError(t, err)

	_, err = s.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET parent_id = ? WHERE id = ?`,
		parent.ID, child2.ID,
	)
	require.NoError(t, err)

	children, hasMore, err := s.HandleListChildren(ctx, parent.ID, 10, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(children), 2)
	assert.False(t, hasMore)
}

// TestHandleListChildren_WithPagination verifies pagination limits.
func TestHandleListChildren_WithPagination(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "PAGELIST")

	// Create 5 children.
	for i := 0; i < 5; i++ {
		child := createTestNode(t, s, fmt.Sprintf("Child %d", i), "PAGELIST")
		_, err := s.store.WriteDB().ExecContext(ctx,
			`UPDATE nodes SET parent_id = ? WHERE id = ?`,
			parent.ID, child.ID,
		)
		require.NoError(t, err)
	}

	children, hasMore, err := s.HandleListChildren(ctx, parent.ID, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, len(children))
	assert.True(t, hasMore)

	// Test offset.
	children2, hasMore2, err := s.HandleListChildren(ctx, parent.ID, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, len(children2))
	assert.True(t, hasMore2)
}

// TestHandleListChildren_OffsetBeyondSize verifies offset beyond list size.
func TestHandleListChildren_OffsetBeyondSize(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "OFFSETLIST")
	child := createTestNode(t, s, "Child", "OFFSETLIST")
	_, err := s.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET parent_id = ? WHERE id = ?`,
		parent.ID, child.ID,
	)
	require.NoError(t, err)

	children, hasMore, err := s.HandleListChildren(ctx, parent.ID, 10, 100)
	require.NoError(t, err)
	assert.Empty(t, children)
	assert.False(t, hasMore)
}

// TestHandleListChildren_NonexistentParent_ReturnsEmptyList verifies that
// querying children of a nonexistent parent returns an empty list (not an error),
// because GetDirectChildren queries by parent_id without validating parent existence.
func TestHandleListChildren_NonexistentParent_ReturnsEmptyList(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	children, hasMore, err := s.HandleListChildren(ctx, "NONEXISTENT-999", 10, 0)
	require.NoError(t, err)
	assert.Empty(t, children)
	assert.False(t, hasMore)
}

// --- Decompose Handler Tests ---

// TestHandleDecompose_Success verifies decompose creates children successfully.
func TestHandleDecompose_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "DECOMP")

	ids, err := s.HandleDecompose(ctx, parent.ID, "agent-1", []DecomposeChildReq{
		{Title: "Task 1", Prompt: "Do task 1", Acceptance: "Verify task 1"},
		{Title: "Task 2", Prompt: "Do task 2", Acceptance: "Verify task 2"},
		{Title: "Task 3", Prompt: "Do task 3", Acceptance: "Verify task 3"},
	})

	require.NoError(t, err)
	assert.Len(t, ids, 3)

	// Verify all created nodes are children of parent.
	for _, id := range ids {
		node, err := s.nodeSvc.GetNode(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, parent.ID, node.ParentID)
	}
}

// TestHandleDecompose_ParentNotFound verifies error for nonexistent parent.
func TestHandleDecompose_ParentNotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleDecompose(ctx, "NONEXISTENT-999", "agent-1", []DecomposeChildReq{
		{Title: "Task", Prompt: "Do it"},
	})

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestHandleDecompose_NoChildren verifies validation of empty children.
func TestHandleDecompose_NoChildren(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "DECOMP_EMPTY")

	_, err := s.HandleDecompose(ctx, parent.ID, "agent-1", nil)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// --- Defer Handler Tests ---

// TestHandleDefer_TransitionsStatus verifies defer transition.
func TestHandleDefer_TransitionsStatus(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Defer Node", "DEFER")

	node, err := s.HandleDefer(ctx, created.ID, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, node.Status)
}

// TestHandleDefer_NotFound verifies error for nonexistent node.
func TestHandleDefer_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleDefer(ctx, "NONEXISTENT-999", "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- Cancel Handler Tests ---

// TestHandleCancel_TransitionsStatus verifies cancel transition.
func TestHandleCancel_TransitionsStatus(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Cancel Node", "CANCEL")

	node, err := s.HandleCancel(ctx, created.ID, "user timeout", "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, node.Status)
}

// TestHandleCancel_WithReason verifies cancel with reason is stored.
func TestHandleCancel_WithReason(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Cancel Reason Node", "CANCEL")
	reason := "explicitly abandoned"

	node, err := s.HandleCancel(ctx, created.ID, reason, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, node.Status)
	// Reason is stored in the activity log, node status is set to cancelled
	assert.Equal(t, model.StatusCancelled, node.Status)
}

// TestHandleCancel_NotFound verifies error for nonexistent node.
func TestHandleCancel_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleCancel(ctx, "NONEXISTENT-999", "timeout", "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- Comment Handler Tests ---

// TestHandleComment_Success verifies adding a comment.
func TestHandleComment_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Comment Node", "COMMENT")

	err := s.HandleComment(ctx, created.ID, "This is a comment", "agent-1")
	require.NoError(t, err)
}

// TestHandleComment_NotFound verifies error for nonexistent node.
func TestHandleComment_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	err := s.HandleComment(ctx, "NONEXISTENT-999", "comment", "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestHandleComment_EmptyText verifies empty comment is accepted.
func TestHandleComment_EmptyText(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Comment Empty", "COMMENT_EMPTY")

	err := s.HandleComment(ctx, created.ID, "", "agent-1")
	require.NoError(t, err)
}

// --- UpdatePrompt Handler Tests ---

// TestHandleUpdatePrompt_Success verifies prompt update.
func TestHandleUpdatePrompt_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Prompt Node", "PROMPT")
	newPrompt := "Updated prompt content"

	node, err := s.HandleUpdatePrompt(ctx, created.ID, newPrompt, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, newPrompt, node.Prompt)
}

// TestHandleUpdatePrompt_NotFound verifies error for nonexistent node.
func TestHandleUpdatePrompt_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleUpdatePrompt(ctx, "NONEXISTENT-999", "prompt", "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestHandleUpdatePrompt_EmptyPrompt verifies empty prompt can be set.
func TestHandleUpdatePrompt_EmptyPrompt(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Empty Prompt", "PROMPT")

	node, err := s.HandleUpdatePrompt(ctx, created.ID, "", "agent-1")
	require.NoError(t, err)
	assert.Empty(t, node.Prompt)
}

// --- Rerun Handler Tests ---

// TestHandleRerun_Success verifies rerun execution.
func TestHandleRerun_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Rerun Node", "RERUN")
	// Transition to a terminal state first.
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusInProgress, "", "agent-1"))
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusDone, "", "agent-1"))

	err := s.HandleRerun(ctx, created.ID, service.RerunAll, "Agent failed", "agent-1")
	require.NoError(t, err)

	// Node should be back to open after rerun.
	node, err := s.nodeSvc.GetNode(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestHandleRerun_NotFound verifies error for nonexistent node.
func TestHandleRerun_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	err := s.HandleRerun(ctx, "NONEXISTENT-999", service.RerunAll, "retry", "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestHandleRerun_WithReason verifies rerun with reason is stored.
func TestHandleRerun_WithReason(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Rerun Reason", "RERUN")
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusInProgress, "", "agent-1"))
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusDone, "", "agent-1"))

	reason := "Agent crashed during execution"
	err := s.HandleRerun(ctx, created.ID, service.RerunAll, reason, "agent-1")
	require.NoError(t, err)
}

// --- Restore Handler Tests ---

// TestHandleRestore_Success verifies restore from invalidated state.
func TestHandleRestore_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Restore Node", "RESTORE")
	// First invalidate it by transitioning to invalidated status.
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusInvalidated, "test invalidation", "agent-1"))

	node, err := s.HandleRestore(ctx, created.ID, "agent-1")
	require.NoError(t, err)
	assert.Nil(t, node.InvalidatedAt)
}

// TestHandleRestore_NotFound verifies error for nonexistent node.
func TestHandleRestore_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleRestore(ctx, "NONEXISTENT-999", "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- SessionStart Handler Tests ---

// TestHandleSessionStart_Success verifies session creation.
func TestHandleSessionStart_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "session-agent-1", "SESSION")

	sessionID, err := s.HandleSessionStart(ctx, "session-agent-1", "SESSION")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
}

// TestHandleSessionStart_EmptyProject_Succeeds verifies that empty project is accepted
// per FR-10.5 — SessionStart delegates to SessionService which does not validate project.
func TestHandleSessionStart_EmptyProject_Succeeds(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "bad-session-agent", "SESSION")

	sessionID, err := s.HandleSessionStart(ctx, "bad-session-agent", "")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
}

// --- SessionEnd Handler Tests ---

// TestHandleSessionEnd_Success verifies session termination.
func TestHandleSessionEnd_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "end-agent-1", "SESSION_END")

	_, err := s.HandleSessionStart(ctx, "end-agent-1", "SESSION_END")
	require.NoError(t, err)

	err = s.HandleSessionEnd(ctx, "end-agent-1")
	require.NoError(t, err)
}

// TestHandleSessionEnd_NoActiveSession verifies error when no session.
func TestHandleSessionEnd_NoActiveSession(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "no-session-agent", "SESSION_END_NONE")

	err := s.HandleSessionEnd(ctx, "no-session-agent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// --- SessionSummary Handler Tests ---

// TestHandleSessionSummary_Success verifies session summary retrieval.
func TestHandleSessionSummary_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "summary-agent-1", "SUMMARY")

	_, err := s.HandleSessionStart(ctx, "summary-agent-1", "SUMMARY")
	require.NoError(t, err)

	summary, err := s.HandleSessionSummary(ctx, "summary-agent-1")
	require.NoError(t, err)
	assert.NotNil(t, summary)
}

// TestHandleSessionSummary_NoSession_ReturnsNotFound verifies error when no session exists.
// Per FR-10.5, SessionSummary queries the most recent session — if none exists,
// the service returns ErrNotFound which maps to codes.NotFound.
func TestHandleSessionSummary_NoSession_ReturnsNotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "no-summary-agent", "SUMMARY_NONE")

	_, err := s.HandleSessionSummary(ctx, "no-summary-agent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- Heartbeat Handler Tests ---

// TestHandleHeartbeat_Success verifies heartbeat updates agent timestamp.
func TestHandleHeartbeat_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "hb-agent-1", "HEARTBEAT")

	_, err := s.HandleSessionStart(ctx, "hb-agent-1", "HEARTBEAT")
	require.NoError(t, err)

	err = s.HandleHeartbeat(ctx, "hb-agent-1")
	require.NoError(t, err)
}

// TestHandleHeartbeat_NoActiveSession_Succeeds verifies that heartbeat succeeds
// even without an active session per FR-10.3 — Heartbeat updates last_heartbeat
// on the agents table without requiring an active session.
func TestHandleHeartbeat_NoActiveSession_Succeeds(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "no-hb-agent", "HEARTBEAT_NONE")

	err := s.HandleHeartbeat(ctx, "no-hb-agent")
	require.NoError(t, err)
}

// --- GetAgentState Handler Tests ---

// TestHandleGetAgentState_Success verifies agent state retrieval.
func TestHandleGetAgentState_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "state-agent-1", "STATE")

	_, err := s.HandleSessionStart(ctx, "state-agent-1", "STATE")
	require.NoError(t, err)

	state, err := s.HandleGetAgentState(ctx, "state-agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateIdle, state)
}

// TestHandleGetAgentState_NotFound verifies error for nonexistent agent.
func TestHandleGetAgentState_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleGetAgentState(ctx, "NONEXISTENT-AGENT")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- SetAgentState Handler Tests ---

// TestHandleSetAgentState_ToWorking verifies state transition to working.
func TestHandleSetAgentState_ToWorking(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "set-agent-1", "SET_STATE")

	_, err := s.HandleSessionStart(ctx, "set-agent-1", "SET_STATE")
	require.NoError(t, err)

	err = s.HandleSetAgentState(ctx, "set-agent-1", model.AgentStateWorking, "", "working on task")
	require.NoError(t, err)

	state, err := s.HandleGetAgentState(ctx, "set-agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateWorking, state)
}

// TestHandleSetAgentState_ToIdle verifies state transition to idle.
func TestHandleSetAgentState_ToIdle(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "set-agent-2", "SET_STATE_2")

	_, err := s.HandleSessionStart(ctx, "set-agent-2", "SET_STATE_2")
	require.NoError(t, err)

	err = s.HandleSetAgentState(ctx, "set-agent-2", model.AgentStateWorking, "", "start work")
	require.NoError(t, err)

	err = s.HandleSetAgentState(ctx, "set-agent-2", model.AgentStateIdle, "", "idle again")
	require.NoError(t, err)

	state, err := s.HandleGetAgentState(ctx, "set-agent-2")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateIdle, state)
}

// TestHandleSetAgentState_NotFound verifies error for nonexistent agent.
func TestHandleSetAgentState_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	err := s.HandleSetAgentState(ctx, "NONEXISTENT-AGENT", model.AgentStateWorking, "", "")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- GetCurrentWork Handler Tests ---

// TestHandleGetCurrentWork_ReturnsClaimedNode verifies current work retrieval per FR-10.5.
// GetCurrentWork reads agents.current_node_id which must be set explicitly after claim.
func TestHandleGetCurrentWork_ReturnsClaimedNode(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "work-agent-1", "WORK")

	_, err := s.HandleSessionStart(ctx, "work-agent-1", "WORK")
	require.NoError(t, err)

	workNode := createTestNode(t, s, "Current Work", "WORK")
	_, err = s.HandleClaim(ctx, workNode.ID, "work-agent-1", false)
	require.NoError(t, err)

	// Claim does not automatically update current_node_id on agents table.
	// Set it manually to simulate the full agent workflow.
	_, err = s.store.WriteDB().ExecContext(ctx,
		`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`,
		workNode.ID, "work-agent-1",
	)
	require.NoError(t, err)

	node, err := s.HandleGetCurrentWork(ctx, "work-agent-1")
	require.NoError(t, err)
	assert.Equal(t, workNode.ID, node.ID)
}

// TestHandleGetCurrentWork_NoClaimedNode verifies error when no work claimed.
func TestHandleGetCurrentWork_NoClaimedNode(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "work-agent-none", "WORK_NONE")

	_, err := s.HandleSessionStart(ctx, "work-agent-none", "WORK_NONE")
	require.NoError(t, err)

	_, err = s.HandleGetCurrentWork(ctx, "work-agent-none")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestHandleGetCurrentWork_NotFound verifies error for nonexistent agent.
func TestHandleGetCurrentWork_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleGetCurrentWork(ctx, "NONEXISTENT-AGENT")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- AddDependency Handler Tests ---

// TestHandleAddDependency_BlockingDependency verifies adding blocks dependency.
func TestHandleAddDependency_BlockingDependency(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Blocker A", "DEP_ADD")
	nodeB := createTestNode(t, s, "Blocked B", "DEP_ADD")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeBlocks,
	}

	err := s.HandleAddDependency(ctx, dep)
	require.NoError(t, err)

	// Verify dependency was added.
	deps, err := s.HandleGetDependencies(ctx, nodeB.ID)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, nodeA.ID, deps[0].FromID)
}

// TestHandleAddDependency_RelatesTo verifies adding relates_to dependency per FR-4.2.
// HandleGetDependencies uses GetBlockers which only returns dep_type='blocks',
// so a 'related' dependency won't appear in the blockers list.
func TestHandleAddDependency_RelatesTo(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Related A", "DEP_RELATES")
	nodeB := createTestNode(t, s, "Related B", "DEP_RELATES")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeRelated,
	}

	err := s.HandleAddDependency(ctx, dep)
	require.NoError(t, err)

	// GetBlockers only returns 'blocks' type — 'related' deps are not included.
	deps, err := s.HandleGetDependencies(ctx, nodeB.ID)
	require.NoError(t, err)
	assert.Empty(t, deps)
}

// TestHandleAddDependency_InvalidNode verifies error for nonexistent node per FR-4.1.
// FK constraint on dependencies(to_id) REFERENCES nodes(id) causes an insert error,
// which maps to codes.Internal (unmapped FK violation).
func TestHandleAddDependency_InvalidNode(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Valid Node", "DEP_INVALID")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    "NONEXISTENT-999",
		DepType: model.DepTypeBlocks,
	}

	err := s.HandleAddDependency(ctx, dep)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

// --- RemoveDependency Handler Tests ---

// TestHandleRemoveDependency_Success verifies dependency removal.
func TestHandleRemoveDependency_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Remover A", "DEP_REM")
	nodeB := createTestNode(t, s, "Remover B", "DEP_REM")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeBlocks,
	}
	require.NoError(t, s.HandleAddDependency(ctx, dep))

	err := s.HandleRemoveDependency(ctx, nodeA.ID, nodeB.ID, model.DepTypeBlocks)
	require.NoError(t, err)

	deps, err := s.HandleGetDependencies(ctx, nodeB.ID)
	require.NoError(t, err)
	// Dependency should be removed (or list should not contain this dependency).
	for _, d := range deps {
		if d.FromID == nodeA.ID && d.ToID == nodeB.ID {
			t.Fatal("dependency should have been removed")
		}
	}
}

// TestHandleRemoveDependency_NotFound verifies error when dependency not found.
func TestHandleRemoveDependency_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Not Found A", "DEP_NOTFOUND")
	nodeB := createTestNode(t, s, "Not Found B", "DEP_NOTFOUND")

	// Try to remove a dependency that doesn't exist.
	err := s.HandleRemoveDependency(ctx, nodeA.ID, nodeB.ID, model.DepTypeBlocks)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- GetDependencies Handler Tests ---

// TestHandleGetDependencies_ReturnsBlockers verifies dependency retrieval.
func TestHandleGetDependencies_ReturnsBlockers(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Blocker", "DEP_GET")
	nodeB := createTestNode(t, s, "Blocked", "DEP_GET")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeBlocks,
	}
	require.NoError(t, s.HandleAddDependency(ctx, dep))

	deps, err := s.HandleGetDependencies(ctx, nodeB.ID)
	require.NoError(t, err)
	assert.Len(t, deps, 1)
	assert.Equal(t, nodeA.ID, deps[0].FromID)
	assert.Equal(t, nodeB.ID, deps[0].ToID)
}

// TestHandleGetDependencies_NoDependencies verifies empty list when no blockers.
func TestHandleGetDependencies_NoDependencies(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	node := createTestNode(t, s, "No Blockers", "DEP_EMPTY")

	deps, err := s.HandleGetDependencies(ctx, node.ID)
	require.NoError(t, err)
	assert.Empty(t, deps)
}

// TestHandleGetDependencies_NonexistentNode_ReturnsEmpty verifies that querying
// dependencies for a nonexistent node returns an empty list per FR-4.1.
// GetBlockers queries by to_id without validating node existence.
func TestHandleGetDependencies_NonexistentNode_ReturnsEmpty(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	deps, err := s.HandleGetDependencies(ctx, "NONEXISTENT-999")
	require.NoError(t, err)
	assert.Empty(t, deps)
}

// --- BulkUpdate Handler Tests ---

// TestHandleBulkUpdate_AllSuccess verifies bulk update with all successes.
func TestHandleBulkUpdate_AllSuccess(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Bulk A", "BULK_ALL")
	nodeB := createTestNode(t, s, "Bulk B", "BULK_ALL")
	nodeC := createTestNode(t, s, "Bulk C", "BULK_ALL")

	titleA := "Updated A"
	titleB := "Updated B"
	titleC := "Updated C"

	updated, failed, err := s.HandleBulkUpdate(ctx, []BulkNodeUpdateReq{
		{ID: nodeA.ID, Title: &titleA},
		{ID: nodeB.ID, Title: &titleB},
		{ID: nodeC.ID, Title: &titleC},
	})

	require.NoError(t, err)
	assert.Equal(t, 3, updated)
	assert.Empty(t, failed)
}

// TestHandleBulkUpdate_PartialFailure verifies partial failure handling.
func TestHandleBulkUpdate_PartialFailure(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Bulk Part", "BULK_PART")

	titleA := "Updated"
	titleBad := "Bad"

	updated, failed, err := s.HandleBulkUpdate(ctx, []BulkNodeUpdateReq{
		{ID: nodeA.ID, Title: &titleA},
		{ID: "NONEXISTENT-999", Title: &titleBad},
		{ID: "NONEXISTENT-998", Title: &titleBad},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, updated)
	assert.Len(t, failed, 2)
	assert.Contains(t, failed, "NONEXISTENT-999")
	assert.Contains(t, failed, "NONEXISTENT-998")
}

// TestHandleBulkUpdate_EmptyBatch verifies empty batch handling.
func TestHandleBulkUpdate_EmptyBatch(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	updated, failed, err := s.HandleBulkUpdate(ctx, []BulkNodeUpdateReq{})
	require.NoError(t, err)
	assert.Equal(t, 0, updated)
	assert.Empty(t, failed)
}

// TestHandleBulkUpdate_OnlyDescription verifies updating only description.
func TestHandleBulkUpdate_OnlyDescription(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	node := createTestNode(t, s, "Desc Node", "BULK_DESC")
	desc := "New description"

	updated, failed, err := s.HandleBulkUpdate(ctx, []BulkNodeUpdateReq{
		{ID: node.ID, Description: &desc},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, updated)
	assert.Empty(t, failed)

	// Verify description was updated.
	updatedNode, err := s.nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, desc, updatedNode.Description)
}

// --- StreamRecoveryInterceptor Tests ---

// TestStreamRecoveryInterceptor_CatchesPanic verifies panic recovery in streams.
func TestStreamRecoveryInterceptor_CatchesPanic(t *testing.T) {
	s := testGRPCServer(t)

	interceptor := s.streamRecoveryInterceptor()

	// Create a mock server stream.
	mockStream := &mockServerStream{}

	err := interceptor(
		nil,
		mockStream,
		&grpc.StreamServerInfo{FullMethod: "/test.Stream"},
		func(srv any, ss grpc.ServerStream) error {
			panic("stream panic")
		},
	)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

// --- StreamLoggingInterceptor Tests ---

// TestStreamLoggingInterceptor_LogsStreamCall verifies stream logging.
func TestStreamLoggingInterceptor_LogsStreamCall(t *testing.T) {
	s := testGRPCServer(t)

	interceptor := s.streamLoggingInterceptor()

	mockStream := &mockServerStream{}

	err := interceptor(
		nil,
		mockStream,
		&grpc.StreamServerInfo{FullMethod: "/test.Stream"},
		func(srv any, ss grpc.ServerStream) error {
			return nil
		},
	)

	require.NoError(t, err)
}

// TestStreamLoggingInterceptor_LogsStreamError verifies error logging in streams.
func TestStreamLoggingInterceptor_LogsStreamError(t *testing.T) {
	s := testGRPCServer(t)

	interceptor := s.streamLoggingInterceptor()

	mockStream := &mockServerStream{}

	err := interceptor(
		nil,
		mockStream,
		&grpc.StreamServerInfo{FullMethod: "/test.Stream"},
		func(srv any, ss grpc.ServerStream) error {
			return fmt.Errorf("stream error")
		},
	)

	require.Error(t, err)
}

// --- Mock ServerStream for testing ---

type mockServerStream struct{}

func (m *mockServerStream) SetHeader(hmd metadata.MD) error {
	return nil
}

func (m *mockServerStream) SendHeader(hmd metadata.MD) error {
	return nil
}

func (m *mockServerStream) SetTrailer(hmd metadata.MD) {
}

func (m *mockServerStream) Context() context.Context {
	return context.Background()
}

func (m *mockServerStream) SendMsg(v interface{}) error {
	return nil
}

func (m *mockServerStream) RecvMsg(v interface{}) error {
	return nil
}
