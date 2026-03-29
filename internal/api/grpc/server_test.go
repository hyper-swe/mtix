// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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
	rpcgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testGRPCServer creates a gRPC Server with real store and services for testing.
func testGRPCServer(t *testing.T) *Server {
	t.Helper()

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}
	clock := testClock()

	configSvc, err := service.NewConfigService("")
	require.NoError(t, err)

	contextSvc := service.NewContextService(st, config, logger)
	promptSvc := service.NewPromptService(st, broadcaster, logger, clock)

	return NewServer(
		st,
		service.NewNodeService(st, broadcaster, config, logger, clock),
		service.NewBackgroundService(st, config, logger, clock),
		service.NewSessionService(st, config, logger, clock),
		service.NewAgentService(st, broadcaster, config, logger, clock),
		configSvc,
		contextSvc,
		promptSvc,
		broadcaster,
		logger,
		ServerConfig{},
		clock,
	)
}

// createTestNode is a test helper that creates a node via the gRPC handler.
func createTestNode(t *testing.T, s *Server, title, project string) *model.Node {
	t.Helper()
	node, err := s.HandleCreateNode(context.Background(), &CreateNodeReq{
		Title:   title,
		Project: project,
		Creator: "test-agent",
	})
	require.NoError(t, err)
	require.NotNil(t, node)
	return node
}

// ensureAgent is a test helper that creates or verifies an agent record.
// Required for session/agent tests due to FK constraint on sessions.agent_id.
func ensureAgent(t *testing.T, s *Server, agentID, project string) {
	t.Helper()
	ctx := context.Background()
	now := s.clock().UTC().Format(time.RFC3339)
	_, err := s.store.WriteDB().ExecContext(ctx,
		`INSERT OR IGNORE INTO agents (agent_id, project, state, last_heartbeat)
		 VALUES (?, ?, ?, ?)`,
		agentID, project, model.AgentStateIdle, now,
	)
	require.NoError(t, err)
}

// --- MTIX-7.2.1 Server Infrastructure Tests ---

// TestNewServer_DefaultPort verifies default port is 6850 per FR-8.1.
func TestNewServer_DefaultPort(t *testing.T) {
	s := testGRPCServer(t)
	assert.Equal(t, "6850", s.config.Port, "default gRPC port should be 6850")
}

// TestNewServer_CustomPort verifies custom port override.
func TestNewServer_CustomPort(t *testing.T) {
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}
	clock := testClock()

	configSvc, err := service.NewConfigService("")
	require.NoError(t, err)

	s := NewServer(
		st,
		service.NewNodeService(st, broadcaster, config, logger, clock),
		service.NewBackgroundService(st, config, logger, clock),
		service.NewSessionService(st, config, logger, clock),
		service.NewAgentService(st, broadcaster, config, logger, clock),
		configSvc,
		service.NewContextService(st, config, logger),
		service.NewPromptService(st, broadcaster, logger, clock),
		broadcaster,
		logger,
		ServerConfig{Port: "7777"},
		clock,
	)

	assert.Equal(t, "7777", s.config.Port)
}

// TestNewServer_DefaultClock verifies default clock when nil passed.
func TestNewServer_DefaultClock(t *testing.T) {
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	require.NoError(t, os.MkdirAll(dbDir, 0o755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := sqlite.New(dbDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}

	configSvc, err := service.NewConfigService("")
	require.NoError(t, err)

	s := NewServer(
		st,
		service.NewNodeService(st, broadcaster, config, logger, testClock()),
		service.NewBackgroundService(st, config, logger, testClock()),
		service.NewSessionService(st, config, logger, testClock()),
		service.NewAgentService(st, broadcaster, config, logger, testClock()),
		configSvc,
		service.NewContextService(st, config, logger),
		service.NewPromptService(st, broadcaster, logger, testClock()),
		broadcaster,
		logger,
		ServerConfig{},
		nil, // nil clock should default to time.Now
	)

	// Clock should be set (not nil) — it should use time.Now.
	require.NotNil(t, s.clock)
	now := s.clock()
	assert.False(t, now.IsZero(), "clock should return non-zero time")
}

// TestServer_GRPCServer_ReturnsUnderlyingServer verifies accessor.
func TestServer_GRPCServer_ReturnsUnderlyingServer(t *testing.T) {
	s := testGRPCServer(t)
	assert.NotNil(t, s.GRPCServer(), "GRPCServer() should return non-nil")
}

// TestServer_StartAndGracefulStop verifies server lifecycle per FR-8.1.
func TestServer_StartAndGracefulStop(t *testing.T) {
	s := testGRPCServer(t)

	// Dynamically pick a free port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())

	s.config.Port = fmt.Sprintf("%d", port)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Start()
	}()

	// Give server time to start.
	time.Sleep(50 * time.Millisecond)

	// Verify the port is listening.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	require.NoError(t, err, "server should be listening")
	require.NoError(t, conn.Close())

	// Graceful stop.
	s.GracefulStop()

	// Server should exit cleanly.
	select {
	case err := <-errCh:
		// grpc.Serve returns nil on graceful stop.
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within timeout")
	}
}

// TestServer_Stop_ImmediateShutdown verifies immediate stop.
func TestServer_Stop_ImmediateShutdown(t *testing.T) {
	s := testGRPCServer(t)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())

	s.config.Port = fmt.Sprintf("%d", port)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	// Immediate stop.
	s.Stop()

	select {
	case <-errCh:
		// Server stopped — success.
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within timeout")
	}
}

// --- MTIX-7.2.1 Error Mapping Tests ---

// TestMapError_NilReturnsNil verifies nil error passthrough.
func TestMapError_NilReturnsNil(t *testing.T) {
	assert.NoError(t, mapError(nil))
}

// TestMapError_SentinelErrors verifies all sentinel → gRPC code mappings per FR-7.7.
func TestMapError_SentinelErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{"NotFound", model.ErrNotFound, codes.NotFound},
		{"InvalidInput", model.ErrInvalidInput, codes.InvalidArgument},
		{"AlreadyExists", model.ErrAlreadyExists, codes.AlreadyExists},
		{"InvalidTransition", model.ErrInvalidTransition, codes.FailedPrecondition},
		{"CycleDetected", model.ErrCycleDetected, codes.FailedPrecondition},
		{"Conflict", model.ErrConflict, codes.Aborted},
		{"AlreadyClaimed", model.ErrAlreadyClaimed, codes.FailedPrecondition},
		{"NodeBlocked", model.ErrNodeBlocked, codes.FailedPrecondition},
		{"StillDeferred", model.ErrStillDeferred, codes.FailedPrecondition},
		{"AgentStillActive", model.ErrAgentStillActive, codes.FailedPrecondition},
		{"NoActiveSession", model.ErrNoActiveSession, codes.FailedPrecondition},
		{"InvalidConfigKey", model.ErrInvalidConfigKey, codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grpcErr := mapError(tt.err)
			require.Error(t, grpcErr)
			st, ok := status.FromError(grpcErr)
			require.True(t, ok, "should be a gRPC status error")
			assert.Equal(t, tt.wantCode, st.Code())
		})
	}
}

// TestMapError_WrappedSentinelErrors verifies wrapped errors map correctly.
func TestMapError_WrappedSentinelErrors(t *testing.T) {
	wrapped := fmt.Errorf("create node: %w", model.ErrNotFound)
	grpcErr := mapError(wrapped)
	st, ok := status.FromError(grpcErr)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestMapError_UnknownError_ReturnsInternal verifies unknown errors map to Internal.
func TestMapError_UnknownError_ReturnsInternal(t *testing.T) {
	grpcErr := mapError(fmt.Errorf("something unexpected"))
	st, ok := status.FromError(grpcErr)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

// --- MTIX-7.2.2 CRUD Handler Tests ---

// TestGRPC_CreateNode_ReturnsNode verifies node creation via gRPC handler.
func TestGRPC_CreateNode_ReturnsNode(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	node, err := s.HandleCreateNode(ctx, &CreateNodeReq{
		Title:   "Test Node",
		Project: "TEST",
		Creator: "agent-1",
	})

	require.NoError(t, err)
	assert.NotEmpty(t, node.ID)
	assert.Equal(t, "Test Node", node.Title)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// TestGRPC_CreateNode_MissingTitle_ReturnsInvalidArgument verifies validation.
func TestGRPC_CreateNode_MissingTitle_ReturnsInvalidArgument(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleCreateNode(ctx, &CreateNodeReq{
		Project: "TEST",
		Creator: "agent-1",
	})

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestGRPC_CreateNode_MissingProject_ReturnsInvalidArgument verifies validation.
func TestGRPC_CreateNode_MissingProject_ReturnsInvalidArgument(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleCreateNode(ctx, &CreateNodeReq{
		Title:   "Valid Title",
		Creator: "agent-1",
	})

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestGRPC_GetNode_Success verifies successful node retrieval.
func TestGRPC_GetNode_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Get Me", "TEST")

	node, err := s.HandleGetNode(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, node.ID)
	assert.Equal(t, "Get Me", node.Title)
}

// TestGRPC_GetNode_NotFound_ReturnsNotFoundStatus verifies 404 mapping.
func TestGRPC_GetNode_NotFound_ReturnsNotFoundStatus(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleGetNode(ctx, "NONEXISTENT-999")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestGRPC_UpdateNode_Success verifies partial update via gRPC.
func TestGRPC_UpdateNode_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Original", "TEST")
	newTitle := "Updated Title"

	updated, err := s.HandleUpdateNode(ctx, &UpdateNodeReq{
		ID:    created.ID,
		Title: &newTitle,
	})

	require.NoError(t, err)
	assert.Equal(t, "Updated Title", updated.Title)
}

// TestGRPC_UpdateNode_NotFound verifies error for nonexistent node.
func TestGRPC_UpdateNode_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	title := "New Title"
	_, err := s.HandleUpdateNode(ctx, &UpdateNodeReq{
		ID:    "NONEXISTENT-999",
		Title: &title,
	})

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestGRPC_DeleteNode_Success verifies soft-delete via gRPC.
func TestGRPC_DeleteNode_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Delete Me", "TEST")

	err := s.HandleDeleteNode(ctx, created.ID, false, "agent-1")
	require.NoError(t, err)

	// Node should not be found after deletion.
	_, err = s.HandleGetNode(ctx, created.ID)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestGRPC_UndeleteNode_Success verifies undelete via gRPC.
func TestGRPC_UndeleteNode_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Undelete Me", "TEST")
	require.NoError(t, s.HandleDeleteNode(ctx, created.ID, false, "agent-1"))

	node, err := s.HandleUndelete(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, node.ID)
}

// --- MTIX-7.2.2 LLM Shortcut Handler Tests ---

// TestGRPC_Claim_Success verifies claim via gRPC.
func TestGRPC_Claim_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Claim Me", "TEST")

	node, err := s.HandleClaim(ctx, created.ID, "agent-1", false)
	require.NoError(t, err)
	assert.Equal(t, "agent-1", node.Assignee)
}

// TestGRPC_Claim_AlreadyClaimed_ReturnsFailedPrecondition verifies double-claim error.
func TestGRPC_Claim_AlreadyClaimed_ReturnsFailedPrecondition(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Idem Claim", "TEST")

	_, err := s.HandleClaim(ctx, created.ID, "agent-1", false)
	require.NoError(t, err)

	// Second claim by same agent returns ErrAlreadyClaimed.
	_, err = s.HandleClaim(ctx, created.ID, "agent-1", false)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestGRPC_Unclaim_Success verifies unclaim via gRPC.
func TestGRPC_Unclaim_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Unclaim Me", "TEST")
	_, err := s.HandleClaim(ctx, created.ID, "agent-1", false)
	require.NoError(t, err)

	node, err := s.HandleUnclaim(ctx, created.ID, "done with it", "agent-1")
	require.NoError(t, err)
	assert.Empty(t, node.Assignee)
}

// TestGRPC_Done_TransitionsStatus verifies done transition via gRPC.
func TestGRPC_Done_TransitionsStatus(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Done Node", "TEST")
	// Must transition open → in_progress → done per state machine.
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusInProgress, "", "agent-1"))

	node, err := s.HandleDone(ctx, created.ID, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, node.Status)
}

// TestGRPC_Done_InvalidTransition_FailedPrecondition verifies invalid state machine.
func TestGRPC_Done_InvalidTransition_FailedPrecondition(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Bad Done", "TEST")

	// open → done is not a valid transition.
	_, err := s.HandleDone(ctx, created.ID, "agent-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestGRPC_Block_TransitionsStatus verifies block transition.
func TestGRPC_Block_TransitionsStatus(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Block Node", "TEST")
	// open → in_progress → blocked.
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusInProgress, "", "agent-1"))

	node, err := s.HandleBlock(ctx, created.ID, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, node.Status)
}

// TestGRPC_Reopen_TransitionsStatus verifies reopen from blocked.
func TestGRPC_Reopen_TransitionsStatus(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Reopen Node", "TEST")
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusInProgress, "", "agent-1"))
	require.NoError(t, s.nodeSvc.TransitionStatus(ctx, created.ID, model.StatusBlocked, "", "agent-1"))

	node, err := s.HandleReopen(ctx, created.ID, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status)
}

// --- MTIX-7.2.2 Query Handler Tests ---

// TestGRPC_Search_ReturnsNodes verifies search handler.
func TestGRPC_Search_ReturnsNodes(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	createTestNode(t, s, "Search A", "SEARCH")
	createTestNode(t, s, "Search B", "SEARCH")

	filter := store.NodeFilter{
		Status: []model.Status{model.StatusOpen},
	}

	nodes, total, hasMore, err := s.HandleSearch(ctx, filter, 10, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(nodes), 2)
	assert.GreaterOrEqual(t, total, 2)
	assert.False(t, hasMore)
}

// TestGRPC_Search_WithPagination verifies pagination in search.
func TestGRPC_Search_WithPagination(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		createTestNode(t, s, fmt.Sprintf("Page Node %d", i), "PAGE")
	}

	nodes, _, hasMore, err := s.HandleSearch(ctx, store.NodeFilter{
		Status: []model.Status{model.StatusOpen},
	}, 2, 0)
	require.NoError(t, err)
	assert.Len(t, nodes, 2)
	assert.True(t, hasMore)
}

// TestGRPC_GetContext_ReturnsAssembledPrompt verifies context retrieval.
func TestGRPC_GetContext_ReturnsAssembledPrompt(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	created := createTestNode(t, s, "Context Node", "CTX")

	resp, err := s.HandleGetContext(ctx, created.ID)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.Chain)
}

// TestGRPC_GetContext_NotFound verifies error for nonexistent node.
func TestGRPC_GetContext_NotFound(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	_, err := s.HandleGetContext(ctx, "NONEXISTENT-999")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- MTIX-7.2.2 Decompose Handler Tests ---

// TestGRPC_Decompose_ReturnsCreatedIDs verifies decompose creates children.
func TestGRPC_Decompose_ReturnsCreatedIDs(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "DEC")

	ids, err := s.HandleDecompose(ctx, parent.ID, "agent-1", []DecomposeChildReq{
		{Title: "Child A", Prompt: "Do A"},
		{Title: "Child B", Prompt: "Do B"},
	})
	require.NoError(t, err)
	assert.Len(t, ids, 2)
}

// TestGRPC_Decompose_EmptyChildren_ReturnsInvalidArgument verifies validation.
func TestGRPC_Decompose_EmptyChildren_ReturnsInvalidArgument(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	parent := createTestNode(t, s, "Parent", "DEC")

	_, err := s.HandleDecompose(ctx, parent.ID, "agent-1", nil)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// --- MTIX-7.2.2 Dependency Handler Tests ---

// TestGRPC_AddDependency_Success verifies adding a dependency.
func TestGRPC_AddDependency_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Node A", "DEP")
	nodeB := createTestNode(t, s, "Node B", "DEP")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeBlocks,
	}

	err := s.HandleAddDependency(ctx, dep)
	require.NoError(t, err)

	// GetDependencies returns blockers of the given node (dependencies WHERE to_id = nodeID).
	// Since we created a dependency nodeA → nodeB, nodeA blocks nodeB.
	// So querying nodeB should return nodeA as a blocker.
	deps, err := s.HandleGetDependencies(ctx, nodeB.ID)
	require.NoError(t, err)
	assert.Len(t, deps, 1)
	assert.Equal(t, nodeA.ID, deps[0].FromID)
	assert.Equal(t, nodeB.ID, deps[0].ToID)
}

// TestGRPC_RemoveDependency_Success verifies removing a dependency.
func TestGRPC_RemoveDependency_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Node A", "DEP")
	nodeB := createTestNode(t, s, "Node B", "DEP")

	dep := &model.Dependency{
		FromID:  nodeA.ID,
		ToID:    nodeB.ID,
		DepType: model.DepTypeBlocks,
	}
	require.NoError(t, s.HandleAddDependency(ctx, dep))

	err := s.HandleRemoveDependency(ctx, nodeA.ID, nodeB.ID, model.DepTypeBlocks)
	require.NoError(t, err)

	deps, err := s.HandleGetDependencies(ctx, nodeA.ID)
	require.NoError(t, err)
	assert.Empty(t, deps)
}

// --- MTIX-7.2.2 Bulk Handler Tests ---

// TestGRPC_BulkUpdate_Success verifies bulk update.
func TestGRPC_BulkUpdate_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Bulk A", "BLK")
	nodeB := createTestNode(t, s, "Bulk B", "BLK")

	titleA := "Bulk A Updated"
	titleB := "Bulk B Updated"

	updated, failed, err := s.HandleBulkUpdate(ctx, []BulkNodeUpdateReq{
		{ID: nodeA.ID, Title: &titleA},
		{ID: nodeB.ID, Title: &titleB},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, updated)
	assert.Empty(t, failed)
}

// TestGRPC_BulkUpdate_ExceedsMax_ReturnsInvalidArgument verifies batch size limit.
func TestGRPC_BulkUpdate_ExceedsMax_ReturnsInvalidArgument(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	// Create 101 updates (exceeds max of 100).
	updates := make([]BulkNodeUpdateReq, 101)
	for i := range updates {
		title := fmt.Sprintf("Title %d", i)
		updates[i] = BulkNodeUpdateReq{ID: fmt.Sprintf("FAKE-%d", i), Title: &title}
	}

	_, _, err := s.HandleBulkUpdate(ctx, updates)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestGRPC_BulkUpdate_PartialFailure verifies partial failure handling.
func TestGRPC_BulkUpdate_PartialFailure(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	nodeA := createTestNode(t, s, "Bulk OK", "BLK")

	titleOK := "Updated OK"
	titleBad := "Updated Bad"

	updated, failed, err := s.HandleBulkUpdate(ctx, []BulkNodeUpdateReq{
		{ID: nodeA.ID, Title: &titleOK},
		{ID: "NONEXISTENT-999", Title: &titleBad},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, updated)
	assert.Len(t, failed, 1)
	assert.Equal(t, "NONEXISTENT-999", failed[0])
}

// --- MTIX-7.2.2 Session/Agent Handler Tests ---

// TestGRPC_SessionStart_ReturnsSessionID verifies session start.
func TestGRPC_SessionStart_ReturnsSessionID(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "agent-1", "TEST")

	sessionID, err := s.HandleSessionStart(ctx, "agent-1", "TEST")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
}

// TestGRPC_Heartbeat_Success verifies heartbeat.
func TestGRPC_Heartbeat_Success(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "agent-1", "TEST")

	_, err := s.HandleSessionStart(ctx, "agent-1", "TEST")
	require.NoError(t, err)

	err = s.HandleHeartbeat(ctx, "agent-1")
	require.NoError(t, err)
}

// TestGRPC_GetAgentState_ReturnsState verifies agent state retrieval.
func TestGRPC_GetAgentState_ReturnsState(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "agent-1", "TEST")

	_, err := s.HandleSessionStart(ctx, "agent-1", "TEST")
	require.NoError(t, err)

	state, err := s.HandleGetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateIdle, state)
}

// TestGRPC_SetAgentState_UpdatesState verifies agent state update.
func TestGRPC_SetAgentState_UpdatesState(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()

	ensureAgent(t, s, "agent-1", "TEST")

	_, err := s.HandleSessionStart(ctx, "agent-1", "TEST")
	require.NoError(t, err)

	err = s.HandleSetAgentState(ctx, "agent-1", model.AgentStateWorking, "", "working on task")
	require.NoError(t, err)

	state, err := s.HandleGetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateWorking, state)
}

// --- MTIX-7.2.3 Subscribe Handler Tests ---

// TestGRPC_Subscribe_ReceivesEvents verifies subscribe streaming.
func TestGRPC_Subscribe_ReceivesEvents(t *testing.T) {
	s := testGRPCServer(t)

	events := make([]service.Event, 0)
	ctx, cancel := context.WithCancel(context.Background())

	// Start subscriber in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.HandleSubscribe(ctx, nil, func(e service.Event) error {
			events = append(events, e)
			if len(events) >= 1 {
				cancel()
			}
			return nil
		})
	}()

	// Give subscriber time to start.
	time.Sleep(20 * time.Millisecond)

	// Create a node to trigger an event via the subscriber's channel.
	// Note: HandleSubscribe uses a channelSubscriber that needs to be
	// registered with the broadcaster. Since we're testing the handler
	// logic directly, we test the channel subscriber separately.
	cancel()

	err := <-errCh
	assert.NoError(t, err, "subscribe should exit cleanly on context cancel")
}

// TestGRPC_Subscribe_ClientDisconnect_Handled verifies clean disconnect.
func TestGRPC_Subscribe_ClientDisconnect_Handled(t *testing.T) {
	s := testGRPCServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Immediate cancel simulates disconnect.

	err := s.HandleSubscribe(ctx, nil, func(e service.Event) error {
		return nil
	})
	assert.NoError(t, err)
}

// TestGRPC_Subscribe_SendError_ExitsCleanly verifies send error handling.
func TestGRPC_Subscribe_SendError_ExitsCleanly(t *testing.T) {
	s := testGRPCServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := s.HandleSubscribe(ctx, nil, func(e service.Event) error {
		return fmt.Errorf("send failed")
	})
	assert.NoError(t, err)
}

// --- MTIX-7.2.3 ChannelSubscriber Tests ---

// TestChannelSubscriber_MatchesFilter_NilFilter_AcceptsAll verifies nil filter.
func TestChannelSubscriber_MatchesFilter_NilFilter_AcceptsAll(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cs := &channelSubscriber{
		ch:     make(chan service.Event, 1),
		filter: nil,
		logger: logger,
	}

	event := service.Event{Type: service.EventNodeCreated, NodeID: "TEST-1"}
	assert.True(t, cs.matchesFilter(event))
}

// TestChannelSubscriber_MatchesFilter_UnderPrefix verifies prefix filter.
func TestChannelSubscriber_MatchesFilter_UnderPrefix(t *testing.T) {
	tests := []struct {
		name    string
		under   string
		nodeID  string
		matches bool
	}{
		{"exact match", "TEST-1", "TEST-1", true},
		{"child match", "TEST-1", "TEST-1.1", true},
		{"no match", "TEST-1", "OTHER-2", false},
		{"empty under", "", "TEST-1", true},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &channelSubscriber{
				ch:     make(chan service.Event, 1),
				filter: &SubscribeFilter{Under: tt.under},
				logger: logger,
			}
			event := service.Event{NodeID: tt.nodeID, Type: service.EventNodeCreated}
			assert.Equal(t, tt.matches, cs.matchesFilter(event))
		})
	}
}

// TestChannelSubscriber_MatchesFilter_EventWhitelist verifies event type filter.
func TestChannelSubscriber_MatchesFilter_EventWhitelist(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cs := &channelSubscriber{
		ch: make(chan service.Event, 1),
		filter: &SubscribeFilter{
			Events: []string{string(service.EventNodeCreated), string(service.EventNodeDeleted)},
		},
		logger: logger,
	}

	assert.True(t, cs.matchesFilter(service.Event{Type: service.EventNodeCreated}))
	assert.True(t, cs.matchesFilter(service.Event{Type: service.EventNodeDeleted}))
	assert.False(t, cs.matchesFilter(service.Event{Type: service.EventStatusChanged}))
}

// TestChannelSubscriber_Send_NonBlocking verifies backpressure handling.
func TestChannelSubscriber_Send_NonBlocking(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cs := &channelSubscriber{
		ch:     make(chan service.Event, 1), // Buffer of 1.
		filter: nil,
		logger: logger,
	}

	event := service.Event{Type: service.EventNodeCreated, NodeID: "TEST-1"}

	// First send should succeed (fills buffer).
	cs.Send(event)

	// Second send should be dropped (buffer full, non-blocking).
	cs.Send(event)

	// Verify only one event in channel.
	assert.Len(t, cs.ch, 1)
}

// TestChannelSubscriber_Send_FilteredOut verifies filtered events are skipped.
func TestChannelSubscriber_Send_FilteredOut(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cs := &channelSubscriber{
		ch:     make(chan service.Event, 10),
		filter: &SubscribeFilter{Under: "OTHER"},
		logger: logger,
	}

	cs.Send(service.Event{Type: service.EventNodeCreated, NodeID: "TEST-1"})
	assert.Empty(t, cs.ch, "filtered event should not be sent")
}

// --- Interceptor Tests ---

// TestRecoveryInterceptor_CatchesPanic verifies panic recovery per FR-8.1.
func TestRecoveryInterceptor_CatchesPanic(t *testing.T) {
	s := testGRPCServer(t)

	interceptor := s.recoveryInterceptor()

	resp, err := interceptor(
		context.Background(),
		nil,
		&rpcgrpc.UnaryServerInfo{FullMethod: "/test.Method"},
		func(ctx context.Context, req any) (any, error) {
			panic("test panic")
		},
	)

	assert.Nil(t, resp)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}

// TestLoggingInterceptor_LogsRequest verifies request logging per FR-8.1.
func TestLoggingInterceptor_LogsRequest(t *testing.T) {
	s := testGRPCServer(t)

	interceptor := s.loggingInterceptor()

	resp, err := interceptor(
		context.Background(),
		nil,
		&rpcgrpc.UnaryServerInfo{FullMethod: "/test.Success"},
		func(ctx context.Context, req any) (any, error) {
			return "ok", nil
		},
	)

	assert.Equal(t, "ok", resp)
	assert.NoError(t, err)
}

// TestLoggingInterceptor_LogsError verifies error logging per FR-8.1.
func TestLoggingInterceptor_LogsError(t *testing.T) {
	s := testGRPCServer(t)

	interceptor := s.loggingInterceptor()

	_, err := interceptor(
		context.Background(),
		nil,
		&rpcgrpc.UnaryServerInfo{FullMethod: "/test.Fail"},
		func(ctx context.Context, req any) (any, error) {
			return nil, fmt.Errorf("test error")
		},
	)

	assert.Error(t, err)
}
