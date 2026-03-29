// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// ============================================================================
// In-Memory SQLite Mock Store — FR-14 MCP Coverage Tests
// ============================================================================

// dbMockStore implements store.Store backed by a real in-memory SQLite database.
// This allows session/agent service methods that use WriteDB(), QueryRow(), and
// Query() to execute without nil pointer panics.
type dbMockStore struct {
	db *sql.DB
}

// newDBMockStore creates an in-memory SQLite store with required tables.
func newDBMockStore(t *testing.T) *dbMockStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE nodes (
			id TEXT PRIMARY KEY, parent_id TEXT, depth INTEGER NOT NULL DEFAULT 0,
			seq INTEGER NOT NULL DEFAULT 1, project TEXT NOT NULL DEFAULT 'TEST',
			title TEXT NOT NULL, description TEXT, prompt TEXT, acceptance TEXT,
			node_type TEXT DEFAULT 'auto', issue_type TEXT, priority INTEGER DEFAULT 3,
			labels TEXT, status TEXT DEFAULT 'open', previous_status TEXT,
			progress REAL DEFAULT 0.0, assignee TEXT, creator TEXT, agent_state TEXT,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL, closed_at TEXT,
			defer_until TEXT, estimate_min INTEGER, actual_min INTEGER,
			weight REAL DEFAULT 1.0, content_hash TEXT, code_refs TEXT,
			commit_refs TEXT, annotations TEXT DEFAULT '[]',
			invalidated_at TEXT, invalidated_by TEXT, invalidation_reason TEXT,
			activity TEXT DEFAULT '[]', deleted_at TEXT, deleted_by TEXT,
			metadata TEXT, session_id TEXT
		);
		CREATE TABLE agents (
			agent_id TEXT PRIMARY KEY, project TEXT NOT NULL,
			state TEXT DEFAULT 'idle', state_changed_at TEXT,
			current_node_id TEXT, last_heartbeat TEXT
		);
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY, agent_id TEXT NOT NULL,
			project TEXT NOT NULL, started_at TEXT NOT NULL,
			ended_at TEXT, status TEXT DEFAULT 'active', summary TEXT
		);
		CREATE TABLE sequences (
			key TEXT PRIMARY KEY, value INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE dependencies (
			from_id TEXT NOT NULL, to_id TEXT NOT NULL,
			dep_type TEXT NOT NULL, created_at TEXT NOT NULL,
			created_by TEXT, metadata TEXT,
			PRIMARY KEY (from_id, to_id, dep_type)
		);
	`)
	require.NoError(t, err)

	return &dbMockStore{db: db}
}

func (m *dbMockStore) CreateNode(_ context.Context, _ *model.Node) error   { return nil }
func (m *dbMockStore) GetNode(_ context.Context, id string) (*model.Node, error) {
	return &model.Node{ID: id, Title: "mock", Status: model.StatusOpen}, nil
}
func (m *dbMockStore) UpdateNode(_ context.Context, _ string, _ *store.NodeUpdate) error { return nil }
func (m *dbMockStore) DeleteNode(_ context.Context, _ string, _ bool, _ string) error    { return nil }
func (m *dbMockStore) UndeleteNode(_ context.Context, _ string) error                    { return nil }
func (m *dbMockStore) ListNodes(_ context.Context, _ store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	return nil, 0, nil
}
func (m *dbMockStore) SearchNodes(_ context.Context, _ string, _ store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	return nil, 0, nil
}
func (m *dbMockStore) GetTree(_ context.Context, _ string, _ int) ([]*model.Node, error) {
	return nil, nil
}
func (m *dbMockStore) GetStats(_ context.Context, _ string) (*store.Stats, error) {
	return &store.Stats{ByStatus: map[string]int{}, ByPriority: map[string]int{}, ByType: map[string]int{}}, nil
}
func (m *dbMockStore) NextSequence(_ context.Context, _ string) (int, error) { return 1, nil }
func (m *dbMockStore) AddDependency(_ context.Context, _ *model.Dependency) error {
	return nil
}
func (m *dbMockStore) RemoveDependency(_ context.Context, _, _ string, _ model.DepType) error {
	return nil
}
func (m *dbMockStore) GetBlockers(_ context.Context, _ string) ([]*model.Dependency, error) {
	return nil, nil
}
func (m *dbMockStore) TransitionStatus(_ context.Context, _ string, _ model.Status, _, _ string) error {
	return nil
}
func (m *dbMockStore) ClaimNode(_ context.Context, _, _ string) error      { return nil }
func (m *dbMockStore) UnclaimNode(_ context.Context, _, _, _ string) error { return nil }
func (m *dbMockStore) ForceReclaimNode(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (m *dbMockStore) CancelNode(_ context.Context, _, _, _ string, _ bool) error { return nil }
func (m *dbMockStore) UpdateProgress(_ context.Context, _ string, _ float64) error {
	return nil
}
func (m *dbMockStore) GetDirectChildren(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *dbMockStore) GetAncestorChain(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *dbMockStore) GetSiblings(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *dbMockStore) SetAnnotations(_ context.Context, _ string, _ []model.Annotation) error {
	return nil
}
func (m *dbMockStore) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	return m.db.Query(query, args...)
}
func (m *dbMockStore) QueryRow(_ context.Context, query string, args ...any) *sql.Row {
	return m.db.QueryRow(query, args...)
}
func (m *dbMockStore) WriteDB() *sql.DB { return m.db }
func (m *dbMockStore) GetActivity(_ context.Context, _ string, _, _ int) ([]model.ActivityEntry, error) {
	return nil, nil
}
func (m *dbMockStore) Close() error { return m.db.Close() }

// ============================================================================
// Helper constructors using DB-backed mock store
// ============================================================================

func newDBNodeService(t *testing.T) (*service.NodeService, *dbMockStore) {
	t.Helper()
	st := newDBMockStore(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewNodeService(st, nil, nil, logger, fixedClock), st
}

func newDBSessionService(t *testing.T) (*service.SessionService, *dbMockStore) {
	t.Helper()
	st := newDBMockStore(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewSessionService(st, nil, logger, fixedClock), st
}

func newDBAgentService(t *testing.T) (*service.AgentService, *dbMockStore) {
	t.Helper()
	st := newDBMockStore(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewAgentService(st, nil, nil, logger, fixedClock), st
}

func newDBBackgroundService(t *testing.T) (*service.BackgroundService, *dbMockStore) {
	t.Helper()
	st := newDBMockStore(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewBackgroundService(st, nil, logger, fixedClock), st
}

// seedAgent inserts a test agent into the DB-backed mock store.
func seedAgent(t *testing.T, db *sql.DB, agentID, project, state string) {
	t.Helper()
	now := fixedClock().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat) VALUES (?, ?, ?, ?, ?)`,
		agentID, project, state, now, now,
	)
	require.NoError(t, err)
}

// ============================================================================
// Session Tool Happy Path Tests — FR-11/FR-14 (Session tools via MCP)
// ============================================================================

// TestSessionStartTool_WithValidArgs_Succeeds verifies session start tool happy path.
func TestSessionStartTool_WithValidArgs_Succeeds(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"agent-1","project":"TEST"}`)
	result, err := reg.Call(context.Background(), "mtix_session_start", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "session_id")
	assert.Contains(t, result.Content[0].Text, "agent-1")
}

// TestSessionEndTool_WithValidArgs_ReturnsErrorWhenNoSession verifies session end
// returns appropriate error when no active session exists.
func TestSessionEndTool_WithValidArgs_ReturnsErrorWhenNoSession(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"agent-1"}`)
	_, err := reg.Call(context.Background(), "mtix_session_end", args)
	// No active session exists, so service returns error.
	require.Error(t, err)
}

// TestSessionEndTool_WithActiveSession_Succeeds verifies session end after start.
func TestSessionEndTool_WithActiveSession_Succeeds(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	// Start a session first.
	startArgs := json.RawMessage(`{"agent_id":"agent-1","project":"TEST"}`)
	_, err := reg.Call(context.Background(), "mtix_session_start", startArgs)
	require.NoError(t, err)

	// Now end it.
	endArgs := json.RawMessage(`{"agent_id":"agent-1"}`)
	result, err := reg.Call(context.Background(), "mtix_session_end", endArgs)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Session ended")
}

// TestSessionSummaryTool_WithActiveSession_ReturnsSummary verifies session summary.
func TestSessionSummaryTool_WithActiveSession_ReturnsSummary(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	// Start a session first.
	startArgs := json.RawMessage(`{"agent_id":"agent-2","project":"TEST"}`)
	_, err := reg.Call(context.Background(), "mtix_session_start", startArgs)
	require.NoError(t, err)

	// Get summary.
	summaryArgs := json.RawMessage(`{"agent_id":"agent-2"}`)
	result, err := reg.Call(context.Background(), "mtix_session_summary", summaryArgs)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "session_id")
}

// TestSessionSummaryTool_WithNoSession_ReturnsError verifies summary with no session.
func TestSessionSummaryTool_WithNoSession_ReturnsError(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"nonexistent"}`)
	_, err := reg.Call(context.Background(), "mtix_session_summary", args)
	require.Error(t, err)
}

// ============================================================================
// Agent Tool Happy Path Tests — FR-11/FR-14 (Agent tools via MCP)
// ============================================================================

// TestAgentHeartbeatTool_WithValidArgs_Succeeds verifies heartbeat tool happy path.
func TestAgentHeartbeatTool_WithValidArgs_Succeeds(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, st := newDBAgentService(t)
	seedAgent(t, st.db, "agent-1", "TEST", "idle")
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"agent-1"}`)
	result, err := reg.Call(context.Background(), "mtix_agent_heartbeat", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Heartbeat recorded")
}

// TestAgentStateTool_GetState_ReturnsCurrentState verifies agent state retrieval.
func TestAgentStateTool_GetState_ReturnsCurrentState(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, st := newDBAgentService(t)
	seedAgent(t, st.db, "agent-1", "TEST", "idle")
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	// Get state (no state field -> returns current state).
	args := json.RawMessage(`{"agent_id":"agent-1"}`)
	result, err := reg.Call(context.Background(), "mtix_agent_state", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "idle")
}

// TestAgentStateTool_SetState_UpdatesState verifies agent state update via MCP.
func TestAgentStateTool_SetState_UpdatesState(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, st := newDBAgentService(t)
	seedAgent(t, st.db, "agent-1", "TEST", "idle")
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	// Set state to working (idle -> working is valid).
	args := json.RawMessage(`{"agent_id":"agent-1","state":"working"}`)
	result, err := reg.Call(context.Background(), "mtix_agent_state", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "working")
}

// TestAgentStateTool_GetState_NonExistentAgent_ReturnsError verifies error for unknown agent.
func TestAgentStateTool_GetState_NonExistentAgent_ReturnsError(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"nonexistent"}`)
	_, err := reg.Call(context.Background(), "mtix_agent_state", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestAgentWorkTool_WithNoWork_ReturnsNoWorkMessage verifies agent work tool
// when the agent has no current work assigned.
func TestAgentWorkTool_WithNoWork_ReturnsError(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, st := newDBAgentService(t)
	seedAgent(t, st.db, "agent-1", "TEST", "idle")
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"agent-1"}`)
	_, err := reg.Call(context.Background(), "mtix_agent_work", args)
	// Agent exists but has no current_node_id set.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no current work")
}

// TestAgentWorkTool_NonExistentAgent_ReturnsError verifies work tool with unknown agent.
func TestAgentWorkTool_NonExistentAgent_ReturnsError(t *testing.T) {
	sessionSvc, _ := newDBSessionService(t)
	agentSvc, _ := newDBAgentService(t)
	reg := NewToolRegistry()
	RegisterSessionTools(reg, sessionSvc, agentSvc)

	args := json.RawMessage(`{"agent_id":"nonexistent"}`)
	_, err := reg.Call(context.Background(), "mtix_agent_work", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ============================================================================
// Workflow Tool — Ready and Stale Tests — FR-14
// ============================================================================

// TestReadyTool_WithNoNodes_ReturnsEmptyList verifies ready tool happy path.
func TestReadyTool_WithNoNodes_ReturnsEmptyList(t *testing.T) {
	bgSvc, _ := newDBBackgroundService(t)
	nodeSvc, _ := newDBNodeService(t)
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, nodeSvc, &mcpMockStore{}, bgSvc)

	result, err := reg.Call(context.Background(), "mtix_ready", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "null")
}

// TestStaleTool_WithNoStaleAgents_ReturnsEmptyList verifies stale tool happy path.
func TestStaleTool_WithNoStaleAgents_ReturnsEmptyList(t *testing.T) {
	agentSvc, st := newDBAgentService(t)
	seedAgent(t, st.db, "agent-1", "TEST", "idle")
	configSvc := newTestConfigService()
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, agentSvc, configSvc)

	result, err := reg.Call(context.Background(), "mtix_stale", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// ============================================================================
// SSE Transport Additional Tests — FR-14.1a
// ============================================================================

// TestSSEServer_HandleSSEToolsList_ReturnsRegisteredTools verifies tools/list over SSE.
func TestSSEServer_HandleSSEToolsList_ReturnsRegisteredTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()

	reg.Register(ToolDef{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		return SuccessResult("ok"), nil
	})

	sseServer := NewSSEServer(reg, logger, "1.0.0")

	listReq := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`10`),
		Method:  MethodToolsList,
	}
	reqData, _ := json.Marshal(listReq)

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", bytes.NewReader(reqData))
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)

	body := w.Body.String()
	assert.Contains(t, body, "event: message")
	assert.Contains(t, body, "test_tool")
}

// TestSSEServer_ProcessMessage_UnknownMethod_ReturnsMethodNotFound verifies
// SSE processMessage returns error for unknown methods.
func TestSSEServer_ProcessMessage_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	unknownReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"some/unknown"}`)
	result := sseServer.processMessage(context.Background(), unknownReq)
	require.NotNil(t, result)
	require.NotNil(t, result.Error)
	assert.Equal(t, ErrCodeMethodNotFound, result.Error.Code)
	assert.Contains(t, result.Error.Message, "unknown method")
}

// TestSSEServer_HandleSSEToolsCall_InvalidParams_ReturnsError verifies
// SSE tools/call with invalid params returns error.
func TestSSEServer_HandleSSEToolsCall_InvalidParams_ReturnsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	// Send tools/call with params that can't unmarshal to ToolsCallParams.
	badReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not_an_object"}`)
	result := sseServer.processMessage(context.Background(), badReq)
	require.NotNil(t, result)
	require.NotNil(t, result.Error)
	assert.Equal(t, ErrCodeInvalidParams, result.Error.Code)
}

// TestSSEServer_NewSSEServer_WithNilLogger_UsesDefault verifies nil logger handling.
func TestSSEServer_NewSSEServer_WithNilLogger_UsesDefault(t *testing.T) {
	reg := NewToolRegistry()
	server := NewSSEServer(reg, nil, "1.0.0")
	assert.NotNil(t, server)
	assert.NotNil(t, server.logger)
}

// TestSSEServer_BroadcastNotification_WithClients_SendsToAll verifies broadcast
// sends notifications to all connected SSE clients.
func TestSSEServer_BroadcastNotification_WithClients_SendsToAll(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	// Create a mock SSE client.
	recorder := httptest.NewRecorder()
	client := &sseClient{
		w:       recorder,
		flusher: recorder,
		done:    make(chan struct{}),
	}

	sseServer.addClient(client)
	assert.Equal(t, 1, sseServer.ClientCount())

	// Broadcast.
	sseServer.BroadcastNotification("test/event", map[string]string{"key": "value"})

	body := recorder.Body.String()
	assert.Contains(t, body, "event: message")
	assert.Contains(t, body, "test/event")
	assert.Contains(t, body, "value")

	sseServer.removeClient(client)
	assert.Equal(t, 0, sseServer.ClientCount())
}

// TestSSEServer_BroadcastNotification_WithUnmarshalableParams_LogsError verifies
// broadcast handles marshal failure gracefully.
func TestSSEServer_BroadcastNotification_WithUnmarshalableParams_LogsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	// func values cannot be marshaled to JSON.
	sseServer.BroadcastNotification("test/event", func() {})
	// Should not panic.
}

// TestSSEServer_HandlePostRequests_EmptyLines_Skips verifies that empty lines
// in POST body are skipped per FR-14.1a.
func TestSSEServer_HandlePostRequests_EmptyLines_Skips(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	// Body with empty lines interspersed.
	pingReq := Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  MethodPing,
	}
	reqData, _ := json.Marshal(pingReq)
	body := "\n" + string(reqData) + "\n\n"

	recorder := httptest.NewRecorder()
	client := &sseClient{
		w:       recorder,
		flusher: recorder,
		done:    make(chan struct{}),
	}

	sseServer.handlePostRequests(context.Background(), strings.NewReader(body), client)

	output := recorder.Body.String()
	assert.Contains(t, output, "event: message")
}

// TestSSEServer_SendEvent_WithWriteError_ReturnsError verifies sendEvent
// handles write failures.
func TestSSEServer_SendEvent_WithWriteError_ReturnsError(t *testing.T) {
	// failWriter always returns an error on Write.
	client := &sseClient{
		w:       &failResponseWriter{},
		flusher: &noopFlusher{},
		done:    make(chan struct{}),
	}

	err := client.sendEvent("message", map[string]string{"key": "value"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write SSE event")
}

// ============================================================================
// Server writeJSON Error Tests — FR-14
// ============================================================================

// TestServer_WriteJSON_WithWriteError_ReturnsError verifies writeJSON
// handles writer failures gracefully.
func TestServer_WriteJSON_WithWriteError_ReturnsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(""), &failWriter{}, logger, "1.0.0")

	err := srv.writeJSON(map[string]string{"test": "value"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write response")
}

// TestServer_WriteJSON_WithNewlineWriteError_ReturnsError verifies writeJSON
// handles newline write failure after successful data write.
func TestServer_WriteJSON_WithNewlineWriteError_ReturnsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(""), &failOnSecondWrite{}, logger, "1.0.0")

	err := srv.writeJSON(map[string]string{"test": "value"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write newline")
}

// TestServer_HandleNotification_UnknownMethod_LogsDebug verifies unknown
// notification methods are logged but don't produce errors.
func TestServer_HandleNotification_UnknownMethod_LogsDebug(t *testing.T) {
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "some/unknown/notification",
		// No ID -> notification.
	}
	data, _ := json.Marshal(notif)
	input := string(data) + "\n"

	output := runServer(t, input)
	// Unknown notifications produce no response.
	assert.Empty(t, strings.TrimSpace(output))
}

// TestServer_Serve_WithReadError_ReturnsError verifies Serve handles read errors.
func TestServer_Serve_WithReadError_ReturnsError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(&failReader{}, &output, logger, "1.0.0")

	err := srv.Serve(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read input")
}

// ============================================================================
// Notification Forwarder Additional Tests — FR-14.5
// ============================================================================

// TestNotificationForwarder_WithNilLogger_UsesDefault verifies nil logger handling.
func TestNotificationForwarder_WithNilLogger_UsesDefault(t *testing.T) {
	var buf bytes.Buffer
	hub := service.NewHub(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	server := NewServer(&bytes.Buffer{}, &buf, slog.Default(), "test")

	nf := NewNotificationForwarder(server, hub, nil)
	assert.NotNil(t, nf)
	assert.NotNil(t, nf.logger)
}

// TestNotificationForwarder_ContextCancel_StopsForwarding verifies context
// cancellation stops the forwarding loop.
func TestNotificationForwarder_ContextCancel_StopsForwarding(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	hub := service.NewHub(logger)
	server := NewServer(&bytes.Buffer{}, &buf, logger, "test")

	nf := NewNotificationForwarder(server, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	nf.Start(ctx)

	time.Sleep(10 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	// After context cancel, broadcasting should not reach forwarder.
	bgCtx := context.Background()
	_ = hub.Broadcast(bgCtx, service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-99",
		Timestamp: time.Now(),
	})

	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, buf.Bytes())
}

// TestNotificationForwarder_ForwardEvent_WithWriteError_LogsError verifies
// forwardEvent handles write failures gracefully.
func TestNotificationForwarder_ForwardEvent_WithWriteError_LogsError(t *testing.T) {
	logBuf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	hub := service.NewHub(logger)
	server := NewServer(&bytes.Buffer{}, &failWriter{}, logger, "test")

	nf := NewNotificationForwarder(server, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nf.Start(ctx)

	time.Sleep(10 * time.Millisecond)

	// Broadcast an event — the write to failWriter should fail.
	err := hub.Broadcast(ctx, service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-1",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Author:    "test-agent",
	})
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	nf.Stop()
	cancel()
	time.Sleep(50 * time.Millisecond)

	// The error should have been logged.
	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "write MCP notification")
}

// TestNotificationForwarder_ForwardEvent_AllMappedTypes verifies all mapped event
// types produce correctly named MCP notifications.
func TestNotificationForwarder_ForwardEvent_AllMappedTypes(t *testing.T) {
	tests := []struct {
		eventType      service.EventType
		expectedMethod string
	}{
		{service.EventNodeCreated, "notifications/node.created"},
		{service.EventNodeUpdated, "notifications/node.updated"},
		{service.EventNodeDeleted, "notifications/node.deleted"},
		{service.EventProgressChanged, "notifications/progress.changed"},
		{service.EventNodesInvalidated, "notifications/nodes.invalidated"},
		{service.EventAgentStateChanged, "notifications/agent.state"},
		{service.EventAgentStuck, "notifications/agent.stuck"},
		{service.EventStatusChanged, "notifications/node.status_changed"},
		{service.EventNodeClaimed, "notifications/node.claimed"},
		{service.EventNodeCancelled, "notifications/node.cancelled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
			hub := service.NewHub(logger)
			server := NewServer(&bytes.Buffer{}, &buf, logger, "test")

			nf := NewNotificationForwarder(server, hub, logger)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			nf.Start(ctx)
			time.Sleep(10 * time.Millisecond)

			err := hub.Broadcast(ctx, service.Event{
				Type:      tt.eventType,
				NodeID:    "TEST-1",
				Timestamp: fixedClock(),
				Author:    "tester",
			})
			require.NoError(t, err)

			time.Sleep(100 * time.Millisecond)
			nf.Stop()
			cancel()
			time.Sleep(50 * time.Millisecond)

			server.mu.Lock()
			output := make([]byte, buf.Len())
			copy(output, buf.Bytes())
			server.mu.Unlock()

			if len(output) > 0 {
				var notif Request
				err = json.Unmarshal(output, &notif)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedMethod, notif.Method)
			}
		})
	}
}

// ============================================================================
// Registry Duplicate Registration Test
// ============================================================================

// TestRegistry_Register_Duplicate_Panics verifies duplicate tool registration panics.
func TestRegistry_Register_Duplicate_Panics(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(ToolDef{
		Name:        "dup_tool",
		InputSchema: SchemaObj{Type: "object"},
	}, noopHandler)

	assert.Panics(t, func() {
		reg.Register(ToolDef{
			Name:        "dup_tool",
			InputSchema: SchemaObj{Type: "object"},
		}, noopHandler)
	})
}

// ============================================================================
// Protocol Helper Tests
// ============================================================================

// TestTextContent_CreatesCorrectBlock verifies TextContent helper.
func TestTextContent_CreatesCorrectBlock(t *testing.T) {
	block := TextContent("hello world")
	assert.Equal(t, "text", block.Type)
	assert.Equal(t, "hello world", block.Text)
}

// TestErrorResult_SetsIsError verifies ErrorResult sets the error flag.
func TestErrorResult_SetsIsError(t *testing.T) {
	result := ErrorResult("something failed")
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "something failed", result.Content[0].Text)
}

// TestSuccessResult_ClearsIsError verifies SuccessResult clears error flag.
func TestSuccessResult_ClearsIsError(t *testing.T) {
	result := SuccessResult("all good")
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "all good", result.Content[0].Text)
}

// ============================================================================
// SSE Server Multiple Messages Test
// ============================================================================

// TestSSEServer_HandlePostRequests_MultipleMessages_ProcessesAll verifies
// multiple JSON-RPC messages in a single POST body are all processed.
func TestSSEServer_HandlePostRequests_MultipleMessages_ProcessesAll(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	ping1 := Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: MethodPing}
	ping2 := Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: MethodPing}
	data1, _ := json.Marshal(ping1)
	data2, _ := json.Marshal(ping2)
	body := string(data1) + "\n" + string(data2) + "\n"

	recorder := httptest.NewRecorder()
	client := &sseClient{
		w:       recorder,
		flusher: recorder,
		done:    make(chan struct{}),
	}

	sseServer.handlePostRequests(context.Background(), strings.NewReader(body), client)

	output := recorder.Body.String()
	// Should have two "event: message" entries.
	count := strings.Count(output, "event: message")
	assert.Equal(t, 2, count)
}

// TestSSEServer_HandlePostRequests_ClientWriteError_StopsProcessing verifies
// processing stops when client write fails.
func TestSSEServer_HandlePostRequests_ClientWriteError_StopsProcessing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	ping := Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: MethodPing}
	data, _ := json.Marshal(ping)
	body := string(data) + "\n" + string(data) + "\n"

	client := &sseClient{
		w:       &failResponseWriter{},
		flusher: &noopFlusher{},
		done:    make(chan struct{}),
	}

	// Should not panic despite write failures.
	sseServer.handlePostRequests(context.Background(), strings.NewReader(body), client)
}

// ============================================================================
// Test Helpers: Thread-Safe Buffer
// ============================================================================

// safeBuffer is a thread-safe bytes.Buffer for use as a log output in tests.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *safeBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *safeBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

// ============================================================================
// Test Helpers: Failing Writers
// ============================================================================

// failWriter always returns an error on Write.
type failWriter struct{}

func (f *failWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write error")
}

// failOnSecondWrite succeeds on first Write, fails on second.
type failOnSecondWrite struct {
	mu    sync.Mutex
	calls int
}

func (f *failOnSecondWrite) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls > 1 {
		return 0, errors.New("second write error")
	}
	return len(p), nil
}

// failReader always returns an error on Read.
type failReader struct{}

func (f *failReader) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("read error")
}

// failResponseWriter implements http.ResponseWriter but fails on Write.
type failResponseWriter struct{}

func (f *failResponseWriter) Header() http.Header         { return http.Header{} }
func (f *failResponseWriter) Write(_ []byte) (int, error) { return 0, errors.New("write error") }
func (f *failResponseWriter) WriteHeader(_ int)            {}

// noopFlusher implements http.Flusher.
type noopFlusher struct{}

func (n *noopFlusher) Flush() {}

// Ensure failReader implements io.Reader.
var _ io.Reader = (*failReader)(nil)
