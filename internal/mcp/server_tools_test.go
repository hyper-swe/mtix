// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// ============================================================================
// Server Tests
// ============================================================================

// TestServer_Serve_WithValidJSONRPC_ProcessesMessage verifies JSON-RPC message processing.
func TestServer_Serve_WithValidJSONRPC_ProcessesMessage(t *testing.T) {
	input := makeRequest(t, 1, "initialize", InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      ClientInfo{Name: "test-client"},
	})

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(input), &output, logger, "1.0.0")

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)
}

// TestServer_Serve_WithMultipleMessages_ProcessesSequentially verifies batched message handling.
func TestServer_Serve_WithMultipleMessages_ProcessesSequentially(t *testing.T) {
	msg1 := makeRequest(t, 1, "ping", nil)
	msg2 := makeRequest(t, 2, "ping", nil)
	input := msg1 + msg2

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(input), &output, logger, "1.0.0")

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	// Should have two responses
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	assert.GreaterOrEqual(t, len(lines), 2)
}

// TestServer_Serve_WithEmptyLines_SkipsAndContinues verifies empty line handling.
func TestServer_Serve_WithEmptyLines_SkipsAndContinues(t *testing.T) {
	input := "\n" + makeRequest(t, 1, "ping", nil) + "\n"

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(input), &output, logger, "1.0.0")

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	assert.Nil(t, resp.Error)
}

// TestServer_Serve_ContextCancellation_ReturnsError verifies context cancellation handling.
func TestServer_Serve_ContextCancellation_StopsReading(t *testing.T) {
	input := makeRequest(t, 1, "ping", nil)

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(input), &output, logger, "1.0.0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := srv.Serve(ctx)
	assert.Error(t, err)
}

// TestServer_HandleMessage_WithInvalidJSONRPC_ReturnsError verifies invalid version handling.
func TestServer_HandleMessage_WithInvalidJSONRPCVersion_ReturnsError(t *testing.T) {
	input := makeRequest(t, 1, "initialize", InitializeParams{})
	// Replace jsonrpc with invalid version
	input = strings.Replace(input, `"jsonrpc":"2.0"`, `"jsonrpc":"1.0"`, 1)

	output := runServer(t, input)
	resp := parseResponse(t, output)

	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrCodeInvalidRequest, resp.Error.Code)
}

// TestServer_HandleNotification_WithoutID_SendsNoResponse verifies notification handling.
func TestServer_HandleNotification_WithoutID_SendsNoResponse(t *testing.T) {
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		// NO ID field
	}
	data, _ := json.Marshal(notif)
	input := string(data) + "\n"

	output := runServer(t, input)

	// Notification should produce no response
	assert.Empty(t, strings.TrimSpace(output))
}

// TestServer_SendNotification_SerializesAndWrites verifies notification serialization.
func TestServer_SendNotification_SerializesAndWrites(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(""), &output, logger, "1.0.0")

	err := srv.SendNotification("test/event", map[string]string{"key": "value"})
	require.NoError(t, err)

	var notif Notification
	err = json.Unmarshal(output.Bytes(), &notif)
	require.NoError(t, err)

	assert.Equal(t, "2.0", notif.JSONRPC)
	assert.Equal(t, "test/event", notif.Method)
}

// TestServer_Registry_ReturnsSameRegistry verifies registry access.
func TestServer_Registry_ReturnsSameRegistry(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	srv := NewServer(strings.NewReader(""), &output, logger, "1.0.0")

	reg1 := srv.Registry()
	reg2 := srv.Registry()

	assert.Same(t, reg1, reg2)
}

// TestServer_HandleInitialize_InvalidParams_ReturnsError verifies param validation.
func TestServer_HandleInitialize_InvalidParams_ReturnsError(t *testing.T) {
	// Send valid JSON but with params that can't unmarshal to InitializeParams (string instead of object).
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":"not_an_object"}` + "\n"

	output := runServer(t, input)
	resp := parseResponse(t, output)

	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrCodeInvalidParams, resp.Error.Code)
}

// TestServer_HandleToolsList_ReturnsToolDefinitions verifies tool listing.
func TestServer_HandleToolsList_ReturnsToolDefinitions(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	input := makeRequest(t, 3, "tools/list", nil)
	var output bytes.Buffer
	srv := NewServer(strings.NewReader(input), &output, logger, "test")

	// Register multiple tools
	srv.Registry().Register(ToolDef{
		Name:        "tool_a",
		Description: "Tool A",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		return SuccessResult("a"), nil
	})

	srv.Registry().Register(ToolDef{
		Name:        "tool_b",
		Description: "Tool B",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		return SuccessResult("b"), nil
	})

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	assert.Nil(t, resp.Error)

	var result ToolsListResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, 2, len(result.Tools))
}

// TestServer_HandleToolsCall_InvalidParams_ReturnsError verifies param validation.
func TestServer_HandleToolsCall_InvalidParams_ReturnsError(t *testing.T) {
	// Send valid JSON but with params that can't unmarshal to ToolsCallParams (number instead of object).
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":42}` + "\n"

	output := runServer(t, input)
	resp := parseResponse(t, output)

	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrCodeInvalidParams, resp.Error.Code)
}

// TestServer_HandleToolsCall_ToolReturnsError_WrapsInResult verifies error wrapping.
func TestServer_HandleToolsCall_ToolReturnsError_WrapsInResult(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	input := makeRequest(t, 4, "tools/call", ToolsCallParams{
		Name:      "failing_tool",
		Arguments: json.RawMessage(`{}`),
	})

	var output bytes.Buffer
	srv := NewServer(strings.NewReader(input), &output, logger, "test")

	srv.Registry().Register(ToolDef{
		Name:        "failing_tool",
		Description: "Fails",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		return ErrorResult("tool failed"), nil
	})

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	assert.Nil(t, resp.Error)

	var result ToolsCallResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))
	assert.True(t, result.IsError)
}

// ============================================================================
// Registry Tests
// ============================================================================

// TestRegistry_Count_ReturnsCorrectCount verifies tool count tracking.
func TestRegistry_Count_ReturnsCorrectCount(t *testing.T) {
	reg := NewToolRegistry()
	assert.Equal(t, 0, reg.Count())

	reg.Register(ToolDef{
		Name:        "t1",
		InputSchema: SchemaObj{Type: "object"},
	}, noopHandler)

	assert.Equal(t, 1, reg.Count())

	reg.Register(ToolDef{
		Name:        "t2",
		InputSchema: SchemaObj{Type: "object"},
	}, noopHandler)

	assert.Equal(t, 2, reg.Count())
}

// TestRegistry_Call_WithValidTool_ExecutesHandler verifies handler execution.
func TestRegistry_Call_WithValidTool_ExecutesHandler(t *testing.T) {
	reg := NewToolRegistry()

	executed := false
	reg.Register(ToolDef{
		Name:        "test",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		executed = true
		return SuccessResult("done"), nil
	})

	result, err := reg.Call(context.Background(), "test", nil)
	require.NoError(t, err)
	assert.True(t, executed)
	assert.False(t, result.IsError)
}

// TestRegistry_Call_PassesArgumentsCorrectly verifies argument passing.
func TestRegistry_Call_PassesArgumentsCorrectly(t *testing.T) {
	reg := NewToolRegistry()

	var receivedArgs json.RawMessage
	reg.Register(ToolDef{
		Name:        "test",
		InputSchema: SchemaObj{Type: "object"},
	}, func(_ context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		receivedArgs = args
		return SuccessResult("ok"), nil
	})

	testArgs := json.RawMessage(`{"key":"value"}`)
	_, err := reg.Call(context.Background(), "test", testArgs)
	require.NoError(t, err)
	assert.Equal(t, testArgs, receivedArgs)
}

// ============================================================================
// Tool Registration Tests (Node Tools)
// ============================================================================

// NOTE: Registration tests for individual tool groups (RegisterNodeTools, etc.) are
// not tested here because these functions require concrete service types
// (*service.NodeService, *service.BackgroundService, etc.), which cannot be mocked.
// Full integration tests with real services are in internal/integration/mcp_test.go.







// ============================================================================
// Tool Registration Tests (Docs Tools)
// ============================================================================

// TestRegisterDocsTools_RegistersAllTools verifies docs tool registration.
func TestRegisterDocsTools_RegistersAllTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	expectedTools := []string{
		"mtix_discover",
		"mtix_docs_generate",
	}

	tools := reg.List()
	toolNames := make(map[string]bool)
	for _, t := range tools {
		toolNames[t.Name] = true
	}

	for _, expected := range expectedTools {
		assert.True(t, toolNames[expected], "expected tool %s to be registered", expected)
	}
}

// TestMtixDiscover_ListsAllTools verifies discover tool.
func TestMtixDiscover_ListsAllTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	args := json.RawMessage(`{}`)
	result, err := reg.Call(context.Background(), "mtix_discover", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "mtix_discover")
}

// TestMtixDocsGenerate_Succeeds verifies docs generate tool.
func TestMtixDocsGenerate_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	args := json.RawMessage(`{"force":true}`)
	result, err := reg.Call(context.Background(), "mtix_docs_generate", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// ============================================================================
// SSE Transport Tests
// ============================================================================

// TestSSEServer_ClientCount_TracksConnections verifies client tracking.
func TestSSEServer_ClientCount_TracksConnections(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	assert.Equal(t, 0, sseServer.ClientCount())
}

// TestSSEServer_ProcessMessage_WithMissingID_IgnoresNotification verifies notification handling.
func TestSSEServer_ProcessMessage_WithMissingID_IgnoresNotification(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	notifData := []byte(`{"jsonrpc":"2.0","method":"test/event"}`)
	result := sseServer.processMessage(context.Background(), notifData)
	assert.Nil(t, result, "notification should return nil response")
}

// TestSSEServer_ProcessMessage_WithInvalidJSON_ReturnsParseError verifies error handling.
func TestSSEServer_ProcessMessage_WithInvalidJSON_ReturnsParseError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	invalidData := []byte(`{invalid json}`)
	result := sseServer.processMessage(context.Background(), invalidData)
	require.NotNil(t, result)
	assert.NotNil(t, result.Error)
	assert.Equal(t, ErrCodeParse, result.Error.Code)
}

// TestSSEServer_HandleSSE_WithGETRequest_KeepsConnectionOpen verifies GET handling.
func TestSSEServer_HandleSSE_WithGETRequest_KeepsConnectionOpen(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reg := NewToolRegistry()
	sseServer := NewSSEServer(reg, logger, "1.0.0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest("GET", "/mcp/sse", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	sseServer.HandleSSE(w, req)
	// Should return without error
}

// ============================================================================
// Mock Store for MCP Tool Tests
// ============================================================================

// mcpMockStore implements store.Store for MCP tool handler tests.
// Returns empty/nil results for all operations so handlers complete without error.
type mcpMockStore struct{}

func (m *mcpMockStore) CreateNode(_ context.Context, _ *model.Node) error   { return nil }
func (m *mcpMockStore) GetNode(_ context.Context, _ string) (*model.Node, error) {
	return &model.Node{ID: "TEST-1", Title: "mock", Status: model.StatusOpen}, nil
}
func (m *mcpMockStore) UpdateNode(_ context.Context, _ string, _ *store.NodeUpdate) error {
	return nil
}
func (m *mcpMockStore) DeleteNode(_ context.Context, _ string, _ bool, _ string) error { return nil }
func (m *mcpMockStore) UndeleteNode(_ context.Context, _ string) error                 { return nil }
func (m *mcpMockStore) ListNodes(_ context.Context, _ store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	return nil, 0, nil
}
func (m *mcpMockStore) SearchNodes(_ context.Context, _ string, _ store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	return nil, 0, nil
}
func (m *mcpMockStore) GetTree(_ context.Context, _ string, _ int) ([]*model.Node, error) {
	return nil, nil
}
func (m *mcpMockStore) GetStats(_ context.Context, _ string) (*store.Stats, error) {
	return &store.Stats{ByStatus: map[string]int{}, ByPriority: map[string]int{}, ByType: map[string]int{}}, nil
}
func (m *mcpMockStore) NextSequence(_ context.Context, _ string) (int, error) { return 1, nil }
func (m *mcpMockStore) AddDependency(_ context.Context, _ *model.Dependency) error {
	return nil
}
func (m *mcpMockStore) RemoveDependency(_ context.Context, _, _ string, _ model.DepType) error {
	return nil
}
func (m *mcpMockStore) GetBlockers(_ context.Context, _ string) ([]*model.Dependency, error) {
	return nil, nil
}
func (m *mcpMockStore) TransitionStatus(_ context.Context, _ string, _ model.Status, _, _ string) error {
	return nil
}
func (m *mcpMockStore) ClaimNode(_ context.Context, _, _ string) error      { return nil }
func (m *mcpMockStore) UnclaimNode(_ context.Context, _, _, _ string) error { return nil }
func (m *mcpMockStore) ForceReclaimNode(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (m *mcpMockStore) CancelNode(_ context.Context, _, _, _ string, _ bool) error { return nil }
func (m *mcpMockStore) UpdateProgress(_ context.Context, _ string, _ float64) error {
	return nil
}
func (m *mcpMockStore) GetDirectChildren(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *mcpMockStore) GetAncestorChain(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *mcpMockStore) GetSiblings(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *mcpMockStore) SetAnnotations(_ context.Context, _ string, _ []model.Annotation) error {
	return nil
}
func (m *mcpMockStore) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, nil
}
func (m *mcpMockStore) QueryRow(_ context.Context, _ string, _ ...any) *sql.Row {
	return nil
}
func (m *mcpMockStore) WriteDB() *sql.DB { return nil }
func (m *mcpMockStore) GetActivity(_ context.Context, _ string, _, _ int) ([]model.ActivityEntry, error) {
	return nil, nil
}
func (m *mcpMockStore) Close() error { return nil }

// fixedClock returns a deterministic clock for testing.
func fixedClock() time.Time {
	return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
}

// newTestNodeService creates a NodeService with mock dependencies for registration tests.
func newTestNodeService() *service.NodeService {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewNodeService(&mcpMockStore{}, nil, nil, logger, fixedClock)
}

// newTestSessionService creates a SessionService with mock dependencies.
func newTestSessionService() *service.SessionService {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewSessionService(&mcpMockStore{}, nil, logger, fixedClock)
}

// newTestAgentService creates an AgentService with mock dependencies.
func newTestAgentService() *service.AgentService {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewAgentService(&mcpMockStore{}, nil, nil, logger, fixedClock)
}

// newTestBackgroundService creates a BackgroundService with mock dependencies.
func newTestBackgroundService() *service.BackgroundService {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewBackgroundService(&mcpMockStore{}, nil, logger, fixedClock)
}

// newTestConfigService creates a ConfigService with defaults.
func newTestConfigService() *service.ConfigService {
	cs, _ := service.NewConfigService("")
	return cs
}

// newTestContextService creates a ContextService with mock dependencies.
func newTestContextService() *service.ContextService {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewContextService(&mcpMockStore{}, nil, logger)
}

// newTestPromptService creates a PromptService with mock dependencies.
func newTestPromptService() *service.PromptService {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return service.NewPromptService(&mcpMockStore{}, nil, logger, fixedClock)
}

// ============================================================================
// Tool Registration Tests — FR-14 (MCP tool surface)
// ============================================================================

// TestRegisterNodeTools_RegistersExpectedTools verifies node tool registration count and names.
func TestRegisterNodeTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	expectedNames := []string{
		"mtix_create", "mtix_show", "mtix_list", "mtix_briefing",
		"mtix_delete", "mtix_undelete", "mtix_decompose", "mtix_update",
	}
	assert.Equal(t, len(expectedNames), reg.Count())

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, toolNames[name], "expected tool %s to be registered", name)
	}
}

// TestRegisterWorkflowTools_RegistersExpectedTools verifies workflow tool registration.
func TestRegisterWorkflowTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	expectedNames := []string{
		"mtix_claim", "mtix_unclaim", "mtix_done", "mtix_defer",
		"mtix_cancel", "mtix_reopen", "mtix_ready", "mtix_blocked",
		"mtix_search", "mtix_rerun",
	}
	assert.Equal(t, len(expectedNames), reg.Count())

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, toolNames[name], "expected tool %s to be registered", name)
	}
}

// TestRegisterSessionTools_RegistersExpectedTools verifies session tool registration.
func TestRegisterSessionTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterSessionTools(reg, newTestSessionService(), newTestAgentService())

	expectedNames := []string{
		"mtix_session_start", "mtix_session_end", "mtix_session_summary",
		"mtix_agent_heartbeat", "mtix_agent_state", "mtix_agent_work",
	}
	assert.Equal(t, len(expectedNames), reg.Count())

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, toolNames[name], "expected tool %s to be registered", name)
	}
}

// TestRegisterAnalyticsTools_RegistersExpectedTools verifies analytics tool registration.
func TestRegisterAnalyticsTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	expectedNames := []string{
		"mtix_stats", "mtix_progress", "mtix_stale", "mtix_orphans",
	}
	assert.Equal(t, len(expectedNames), reg.Count())

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, toolNames[name], "expected tool %s to be registered", name)
	}
}

// TestRegisterContextTools_RegistersExpectedTools verifies context tool registration.
func TestRegisterContextTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService())

	expectedNames := []string{
		"mtix_context", "mtix_prompt", "mtix_annotate", "mtix_resolve_annotation",
	}
	assert.Equal(t, len(expectedNames), reg.Count())

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, toolNames[name], "expected tool %s to be registered", name)
	}
}

// TestRegisterDepTools_RegistersExpectedTools verifies dependency tool registration.
func TestRegisterDepTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	expectedNames := []string{
		"mtix_dep_add", "mtix_dep_remove", "mtix_dep_show",
	}
	assert.Equal(t, len(expectedNames), reg.Count())

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, toolNames[name], "expected tool %s to be registered", name)
	}
}

// ============================================================================
// Dependency Tool Handler Tests — FR-14 (dep tools use Store directly)
// ============================================================================

// TestDepAddTool_WithValidArgs_Succeeds verifies dep_add handler happy path.
func TestDepAddTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	args := json.RawMessage(`{"from_id":"A-1","to_id":"A-2","dep_type":"blocks"}`)
	result, err := reg.Call(context.Background(), "mtix_dep_add", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Dependency added")
}

// TestDepAddTool_WithInvalidJSON_ReturnsError verifies dep_add parse error.
func TestDepAddTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	args := json.RawMessage(`{invalid}`)
	_, err := reg.Call(context.Background(), "mtix_dep_add", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse dep_add args")
}

// TestDepRemoveTool_WithValidArgs_Succeeds verifies dep_remove handler happy path.
func TestDepRemoveTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	args := json.RawMessage(`{"from_id":"A-1","to_id":"A-2","dep_type":"blocks"}`)
	result, err := reg.Call(context.Background(), "mtix_dep_remove", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Dependency removed")
}

// TestDepRemoveTool_WithInvalidJSON_ReturnsError verifies dep_remove parse error.
func TestDepRemoveTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	args := json.RawMessage(`not-json`)
	_, err := reg.Call(context.Background(), "mtix_dep_remove", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse dep_remove args")
}

// TestDepShowTool_WithValidArgs_Succeeds verifies dep_show handler happy path.
func TestDepShowTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	args := json.RawMessage(`{"id":"A-1"}`)
	result, err := reg.Call(context.Background(), "mtix_dep_show", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestDepShowTool_WithInvalidJSON_ReturnsError verifies dep_show parse error.
func TestDepShowTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDepTools(reg, &mcpMockStore{})

	args := json.RawMessage(`not-json`)
	_, err := reg.Call(context.Background(), "mtix_dep_show", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse dep_show args")
}

// ============================================================================
// Workflow Tool Handler Tests — FR-14 (store-direct tools)
// ============================================================================

// TestClaimTool_WithValidArgs_Succeeds verifies claim handler happy path.
func TestClaimTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1","agent_id":"agent-1"}`)
	result, err := reg.Call(context.Background(), "mtix_claim", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Claimed")
}

// TestClaimTool_WithInvalidJSON_ReturnsError verifies claim parse error.
func TestClaimTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{bad}`)
	_, err := reg.Call(context.Background(), "mtix_claim", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse claim args")
}

// TestUnclaimTool_WithValidArgs_Succeeds verifies unclaim handler happy path.
func TestUnclaimTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1","reason":"done working"}`)
	result, err := reg.Call(context.Background(), "mtix_unclaim", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Unclaimed")
}

// TestUnclaimTool_WithInvalidJSON_ReturnsError verifies unclaim parse error.
func TestUnclaimTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{bad}`)
	_, err := reg.Call(context.Background(), "mtix_unclaim", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse unclaim args")
}

// TestCancelTool_WithValidArgs_Succeeds verifies cancel handler happy path.
func TestCancelTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1","reason":"no longer needed","cascade":false}`)
	result, err := reg.Call(context.Background(), "mtix_cancel", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Cancelled")
}

// TestCancelTool_WithInvalidJSON_ReturnsError verifies cancel parse error.
func TestCancelTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{bad}`)
	_, err := reg.Call(context.Background(), "mtix_cancel", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse cancel args")
}

// TestSearchTool_WithValidArgs_Succeeds verifies search handler happy path.
func TestSearchTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"status":"open","limit":10}`)
	result, err := reg.Call(context.Background(), "mtix_search", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "total")
}

// TestSearchTool_WithNilArgs_UsesDefaults verifies search with empty args.
func TestSearchTool_WithNilArgs_UsesDefaults(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	result, err := reg.Call(context.Background(), "mtix_search", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestBlockedTool_WithID_ShowsBlockersForNode verifies blocked with specific node.
func TestBlockedTool_WithID_ShowsBlockersForNode(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_blocked", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestBlockedTool_WithoutID_ListsAllBlocked verifies blocked listing.
func TestBlockedTool_WithoutID_ListsAllBlocked(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{}`)
	result, err := reg.Call(context.Background(), "mtix_blocked", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestBlockedTool_WithNilArgs_ListsAllBlocked verifies blocked with nil args.
func TestBlockedTool_WithNilArgs_ListsAllBlocked(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	result, err := reg.Call(context.Background(), "mtix_blocked", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// ============================================================================
// Analytics Tool Handler Tests — FR-14 (analytics use Store directly)
// ============================================================================

// TestStatsTool_WithValidArgs_Succeeds verifies stats handler happy path.
func TestStatsTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	args := json.RawMessage(`{"under":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_stats", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "total")
}

// TestStatsTool_WithNilArgs_ReturnsGlobalStats verifies stats with no scope.
func TestStatsTool_WithNilArgs_ReturnsGlobalStats(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	result, err := reg.Call(context.Background(), "mtix_stats", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestProgressTool_WithValidArgs_Succeeds verifies progress handler happy path.
func TestProgressTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_progress", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "progress")
}

// TestProgressTool_WithInvalidJSON_ReturnsError verifies progress parse error.
func TestProgressTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	args := json.RawMessage(`{bad}`)
	_, err := reg.Call(context.Background(), "mtix_progress", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse progress args")
}

// TestOrphansTool_WithValidArgs_Succeeds verifies orphans handler happy path.
func TestOrphansTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	args := json.RawMessage(`{"limit":25}`)
	result, err := reg.Call(context.Background(), "mtix_orphans", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "count")
}

// TestOrphansTool_WithNilArgs_UsesDefaultLimit verifies orphans defaults.
func TestOrphansTool_WithNilArgs_UsesDefaultLimit(t *testing.T) {
	reg := NewToolRegistry()
	RegisterAnalyticsTools(reg, &mcpMockStore{}, newTestAgentService(), newTestConfigService())

	result, err := reg.Call(context.Background(), "mtix_orphans", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// ============================================================================
// Node Tool Handler Parse Error Tests — FR-14
// ============================================================================

// TestNodeToolHandlers_WithInvalidJSON_ReturnParseError verifies parse errors
// for all node tool handlers using table-driven tests.
func TestNodeToolHandlers_WithInvalidJSON_ReturnParseError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	tests := []struct {
		name     string
		toolName string
		wantMsg  string
	}{
		{"create invalid json", "mtix_create", "parse create args"},
		{"show invalid json", "mtix_show", "parse show args"},
		{"delete invalid json", "mtix_delete", "parse delete args"},
		{"undelete invalid json", "mtix_undelete", "parse undelete args"},
		{"decompose invalid json", "mtix_decompose", "parse decompose args"},
		{"update invalid json", "mtix_update", "parse update args"},
	}

	invalidJSON := json.RawMessage(`{bad json}`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reg.Call(context.Background(), tt.toolName, invalidJSON)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// TestListTool_WithInvalidJSON_ReturnsError verifies list parse error.
func TestListTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{bad}`)
	_, err := reg.Call(context.Background(), "mtix_list", args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse list args")
}

// TestListTool_WithValidArgs_Succeeds verifies list handler happy path.
func TestListTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"status":"open","limit":10}`)
	result, err := reg.Call(context.Background(), "mtix_list", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "total")
}

// TestListTool_WithNilArgs_UsesDefaults verifies list with nil args.
func TestListTool_WithNilArgs_UsesDefaults(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	result, err := reg.Call(context.Background(), "mtix_list", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// ============================================================================
// Workflow Tool Handler Parse Error Tests — FR-14
// ============================================================================

// TestWorkflowToolHandlers_WithInvalidJSON_ReturnParseError verifies parse errors
// for workflow tool handlers using table-driven tests.
func TestWorkflowToolHandlers_WithInvalidJSON_ReturnParseError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	tests := []struct {
		name     string
		toolName string
		wantMsg  string
	}{
		{"done invalid json", "mtix_done", "parse done args"},
		{"defer invalid json", "mtix_defer", "parse defer args"},
		{"reopen invalid json", "mtix_reopen", "parse reopen args"},
		{"rerun invalid json", "mtix_rerun", "parse rerun args"},
	}

	invalidJSON := json.RawMessage(`{bad json}`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reg.Call(context.Background(), tt.toolName, invalidJSON)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// ============================================================================
// Session Tool Handler Parse Error Tests — FR-14
// ============================================================================

// TestSessionToolHandlers_WithInvalidJSON_ReturnParseError verifies parse errors
// for session tool handlers using table-driven tests.
func TestSessionToolHandlers_WithInvalidJSON_ReturnParseError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterSessionTools(reg, newTestSessionService(), newTestAgentService())

	tests := []struct {
		name     string
		toolName string
		wantMsg  string
	}{
		{"session_start invalid json", "mtix_session_start", "parse session_start args"},
		{"session_end invalid json", "mtix_session_end", "parse session_end args"},
		{"session_summary invalid json", "mtix_session_summary", "parse session_summary args"},
		{"heartbeat invalid json", "mtix_agent_heartbeat", "parse heartbeat args"},
		{"agent_state invalid json", "mtix_agent_state", "parse agent_state args"},
		{"agent_work invalid json", "mtix_agent_work", "parse agent_work args"},
	}

	invalidJSON := json.RawMessage(`{bad json}`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reg.Call(context.Background(), tt.toolName, invalidJSON)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// ============================================================================
// Context Tool Handler Parse Error Tests — FR-14
// ============================================================================

// TestContextToolHandlers_WithInvalidJSON_ReturnParseError verifies parse errors
// for context tool handlers using table-driven tests.
func TestContextToolHandlers_WithInvalidJSON_ReturnParseError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService())

	tests := []struct {
		name     string
		toolName string
		wantMsg  string
	}{
		{"context invalid json", "mtix_context", "parse context args"},
		{"prompt invalid json", "mtix_prompt", "parse prompt args"},
		{"annotate invalid json", "mtix_annotate", "parse annotate args"},
		{"resolve_annotation invalid json", "mtix_resolve_annotation", "parse resolve annotation args"},
	}

	invalidJSON := json.RawMessage(`{bad json}`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reg.Call(context.Background(), tt.toolName, invalidJSON)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// ============================================================================
// Docs Tool Additional Tests — FR-14.4
// ============================================================================

// TestMtixDocsGenerate_WithoutForce_Succeeds verifies non-force docs generation.
func TestMtixDocsGenerate_WithoutForce_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	args := json.RawMessage(`{}`)
	result, err := reg.Call(context.Background(), "mtix_docs_generate", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Documentation generation requested")
}

// TestMtixDocsGenerate_WithForce_IncludesForcedMessage verifies forced regen.
func TestMtixDocsGenerate_WithForce_IncludesForcedMessage(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	args := json.RawMessage(`{"force":true}`)
	result, err := reg.Call(context.Background(), "mtix_docs_generate", args)
	require.NoError(t, err)
	assert.Contains(t, result.Content[0].Text, "Forced")
}

// TestMtixDocsGenerate_WithNilArgs_Succeeds verifies nil args handling.
func TestMtixDocsGenerate_WithNilArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	result, err := reg.Call(context.Background(), "mtix_docs_generate", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestMtixDiscover_IncludesToolCount verifies discover output format.
func TestMtixDiscover_IncludesToolCount(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)

	result, err := reg.Call(context.Background(), "mtix_discover", nil)
	require.NoError(t, err)
	assert.Contains(t, result.Content[0].Text, "count")
	assert.Contains(t, result.Content[0].Text, "tools")
}

// ============================================================================
// Tool Schema Validation Tests — FR-14 (verify tool definitions)
// ============================================================================

// TestToolDefs_HaveDescriptions verifies all registered tools have descriptions.
func TestToolDefs_HaveDescriptions(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)
	RegisterDepTools(reg, &mcpMockStore{})
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	for _, td := range reg.List() {
		assert.NotEmpty(t, td.Description, "tool %s missing description", td.Name)
		assert.NotEmpty(t, td.InputSchema.Type, "tool %s missing schema type", td.Name)
	}
}

// TestToolDefs_HaveObjectSchema verifies all tools use object input schema.
func TestToolDefs_HaveObjectSchema(t *testing.T) {
	reg := NewToolRegistry()
	RegisterDocsTools(reg)
	RegisterDepTools(reg, &mcpMockStore{})

	for _, td := range reg.List() {
		assert.Equal(t, "object", td.InputSchema.Type,
			"tool %s should have object schema", td.Name)
	}
}

// ============================================================================
// Server Method Not Found Test
// ============================================================================

// TestServer_HandleRequest_UnknownMethod_ReturnsMethodNotFound verifies unknown methods.
func TestServer_HandleRequest_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	input := makeRequest(t, 1, "unknown/method", nil)
	output := runServer(t, input)
	resp := parseResponse(t, output)

	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrCodeMethodNotFound, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "method not found")
}

// TestServer_HandleToolsCall_UnknownTool_ReturnsError verifies unknown tool call.
func TestServer_HandleToolsCall_UnknownTool_ReturnsError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	input := makeRequest(t, 1, "tools/call", ToolsCallParams{
		Name:      "nonexistent_tool",
		Arguments: json.RawMessage(`{}`),
	})

	var output bytes.Buffer
	srv := NewServer(strings.NewReader(input), &output, logger, "test")

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	resp := parseResponse(t, output.String())
	// Unknown tool should be wrapped as a result with error message.
	assert.Nil(t, resp.Error)
	var result ToolsCallResult
	data, _ := json.Marshal(resp.Result)
	require.NoError(t, json.Unmarshal(data, &result))
	assert.True(t, result.IsError)
}

// ============================================================================
// Node Tool Handler Happy Path Tests — FR-14
// ============================================================================

// TestShowTool_WithValidArgs_ReturnsNode verifies show handler happy path.
func TestShowTool_WithValidArgs_ReturnsNode(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_show", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "TEST-1")
}

// TestDeleteTool_WithValidArgs_Succeeds verifies delete handler happy path.
func TestDeleteTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"id":"TEST-1","cascade":false}`)
	result, err := reg.Call(context.Background(), "mtix_delete", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Deleted TEST-1")
}

// TestUndeleteTool_WithValidArgs_Succeeds verifies undelete handler happy path.
func TestUndeleteTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_undelete", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Undeleted TEST-1")
}

// TestUpdateTool_WithValidArgs_Succeeds verifies update handler happy path.
func TestUpdateTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"id":"TEST-1","title":"New Title","priority":2}`)
	result, err := reg.Call(context.Background(), "mtix_update", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Updated TEST-1")
}

// TestUpdateTool_WithEmptyFields_Succeeds verifies update with no fields set.
func TestUpdateTool_WithEmptyFields_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_update", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestCreateTool_WithValidArgs_Succeeds verifies create handler happy path.
func TestCreateTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"title":"Test Task","project":"TEST","description":"desc","priority":3}`)
	result, err := reg.Call(context.Background(), "mtix_create", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "TEST")
}

// TestCreateTool_WithDefaultPriority_UsesMedium verifies default priority.
func TestCreateTool_WithDefaultPriority_UsesMedium(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"title":"No Priority","project":"TEST"}`)
	result, err := reg.Call(context.Background(), "mtix_create", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestDecomposeTool_WithValidArgs_Succeeds verifies decompose handler.
func TestDecomposeTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	args := json.RawMessage(`{"parent_id":"TEST-1","children":[{"title":"Child 1"},{"title":"Child 2"}]}`)
	result, err := reg.Call(context.Background(), "mtix_decompose", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "created")
}

// ============================================================================
// Workflow Tool Handler Happy Path Tests — FR-14
// ============================================================================

// TestDeferTool_WithValidArgs_Succeeds verifies defer handler (open -> deferred is valid).
func TestDeferTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_defer", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Deferred TEST-1")
}

// TestDoneTool_WithValidArgs_ReturnsTransitionError verifies done handler
// parses args and delegates to service (which rejects open -> done transition).
func TestDoneTool_WithValidArgs_ReturnsTransitionError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1"}`)
	_, err := reg.Call(context.Background(), "mtix_done", args)
	// open -> done is not a valid transition, so service returns error.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid transition")
}

// TestReopenTool_WithValidArgs_ReturnsTransitionError verifies reopen handler
// parses args and delegates to service (which rejects open -> open as noop).
func TestReopenTool_WithValidArgs_ReopenFromOpen(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1"}`)
	// open -> open is idempotent (no error) per FR-7.7a.
	result, err := reg.Call(context.Background(), "mtix_reopen", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Reopened")
}

// TestRerunTool_WithValidArgs_Succeeds verifies rerun handler with defaults.
func TestRerunTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1"}`)
	result, err := reg.Call(context.Background(), "mtix_rerun", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Rerun TEST-1 with strategy all")
}

// TestRerunTool_WithCustomStrategy_Succeeds verifies rerun with explicit strategy.
func TestRerunTool_WithCustomStrategy_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, newTestNodeService(), &mcpMockStore{}, newTestBackgroundService())

	args := json.RawMessage(`{"id":"TEST-1","strategy":"open_only","reason":"test rerun"}`)
	result, err := reg.Call(context.Background(), "mtix_rerun", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "open_only")
}

// ============================================================================
// Context Tool Handler Happy Path Tests — FR-14
// ============================================================================

// TestContextTool_WithValidArgs_ReturnsServiceError verifies context handler
// delegates to ContextService which requires ancestor chain data.
func TestContextTool_WithValidArgs_ReturnsServiceError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService())

	args := json.RawMessage(`{"id":"TEST-1","max_tokens":500}`)
	_, err := reg.Call(context.Background(), "mtix_context", args)
	// Service returns error because mock store returns empty ancestor chain.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestPromptTool_WithValidArgs_Succeeds verifies prompt handler happy path.
func TestPromptTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService())

	args := json.RawMessage(`{"id":"TEST-1","text":"new prompt","author":"test"}`)
	result, err := reg.Call(context.Background(), "mtix_prompt", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Prompt updated")
}

// TestAnnotateTool_WithValidArgs_Succeeds verifies annotate handler happy path.
func TestAnnotateTool_WithValidArgs_Succeeds(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService())

	args := json.RawMessage(`{"id":"TEST-1","text":"note","author":"tester"}`)
	result, err := reg.Call(context.Background(), "mtix_annotate", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Annotation added")
}

// TestResolveAnnotationTool_WithValidArgs_ReturnsServiceError verifies resolve handler
// delegates to PromptService which requires annotation data.
func TestResolveAnnotationTool_WithValidArgs_ReturnsServiceError(t *testing.T) {
	reg := NewToolRegistry()
	RegisterContextTools(reg, newTestContextService(), newTestPromptService())

	args := json.RawMessage(`{"id":"TEST-1","annotation_id":"01ABC","author":"tester"}`)
	_, err := reg.Call(context.Background(), "mtix_resolve_annotation", args)
	// Service returns error because mock store has no annotation data.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ============================================================================
// Helper Functions
// ============================================================================

func noopHandler(_ context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
	return SuccessResult("ok"), nil
}

func makeRequest(t *testing.T, id int, method string, params any) string {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		data, err := json.Marshal(params)
		require.NoError(t, err)
		req["params"] = json.RawMessage(data)
	}
	line, err := json.Marshal(req)
	require.NoError(t, err)
	return string(line) + "\n"
}

func runServer(t *testing.T, input string) string {
	t.Helper()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	var output bytes.Buffer
	srv := NewServer(strings.NewReader(input), &output, logger, "test")

	err := srv.Serve(context.Background())
	require.NoError(t, err)

	return output.String()
}

func parseResponse(t *testing.T, output string) Response {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.NotEmpty(t, lines, "no output from server")

	var resp Response
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &resp))
	return resp
}

// ============================================================================
// Briefing Tool Tests — FR-17.7
// ============================================================================

// TestBriefingTool_WithValidArgs_ReturnsBriefingText verifies that the
// mtix_briefing MCP tool returns plain text in briefing format.
func TestBriefingTool_WithValidArgs_ReturnsBriefingText(t *testing.T) {
	reg := NewToolRegistry()
	mockStore := &briefingMockStore{
		nodes: []*model.Node{
			{ID: "PROJ-1", Title: "First task", Status: model.StatusOpen,
				NodeType: model.NodeTypeEpic, Priority: 1,
				Description: "Test description", Prompt: "Test prompt"},
		},
	}
	registerBriefingTool(reg, mockStore)

	args := json.RawMessage(`{"status":"open","limit":10}`)
	result, err := reg.Call(context.Background(), "mtix_briefing", args)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	// Briefing output should be plain text with separators and labels.
	assert.Contains(t, result.Content[0].Text, "ID: PROJ-1")
	assert.Contains(t, result.Content[0].Text, "TITLE: First task")
	assert.Contains(t, result.Content[0].Text, "PROMPT:")
	assert.Contains(t, result.Content[0].Text, "====")
}

// TestBriefingTool_WithFields_RestrictsOutput verifies field restriction.
func TestBriefingTool_WithFields_RestrictsOutput(t *testing.T) {
	reg := NewToolRegistry()
	mockStore := &briefingMockStore{
		nodes: []*model.Node{
			{ID: "PROJ-1", Title: "Task", Status: model.StatusDone,
				NodeType: model.NodeTypeEpic, Priority: 2, Prompt: "Build it"},
		},
	}
	registerBriefingTool(reg, mockStore)

	args := json.RawMessage(`{"fields":"id,title,prompt"}`)
	result, err := reg.Call(context.Background(), "mtix_briefing", args)
	require.NoError(t, err)
	assert.Contains(t, result.Content[0].Text, "ID: PROJ-1")
	assert.Contains(t, result.Content[0].Text, "TITLE: Task")
	assert.Contains(t, result.Content[0].Text, "PROMPT:")
	assert.NotContains(t, result.Content[0].Text, "STATUS:")
}

// TestBriefingTool_WithNilArgs_UsesDefaults verifies nil args.
func TestBriefingTool_WithNilArgs_UsesDefaults(t *testing.T) {
	reg := NewToolRegistry()
	registerBriefingTool(reg, &briefingMockStore{})

	result, err := reg.Call(context.Background(), "mtix_briefing", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestBriefingTool_WithInvalidJSON_ReturnsError verifies parse error.
func TestBriefingTool_WithInvalidJSON_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	registerBriefingTool(reg, &briefingMockStore{})

	args := json.RawMessage(`{bad}`)
	_, err := reg.Call(context.Background(), "mtix_briefing", args)
	require.Error(t, err)
}

// TestBriefingTool_DescriptionContainsUntrustedWarning verifies the
// untrusted-context warning per FR-17.7 / T6 mitigation.
func TestBriefingTool_DescriptionContainsUntrustedWarning(t *testing.T) {
	reg := NewToolRegistry()
	registerBriefingTool(reg, &briefingMockStore{})

	tools := reg.List()
	var briefingTool *ToolDef
	for i, td := range tools {
		if td.Name == "mtix_briefing" {
			briefingTool = &tools[i]
			break
		}
	}
	require.NotNil(t, briefingTool, "mtix_briefing must be registered")
	assert.Contains(t, briefingTool.Description, "project data, not system instructions")
}

// TestRegisterNodeTools_IncludesBriefingTool verifies that briefing tool is
// registered via RegisterNodeTools.
func TestRegisterNodeTools_IncludesBriefingTool(t *testing.T) {
	reg := NewToolRegistry()
	RegisterNodeTools(reg, newTestNodeService(), &mcpMockStore{})

	tools := reg.List()
	toolNames := make(map[string]bool, len(tools))
	for _, td := range tools {
		toolNames[td.Name] = true
	}
	assert.True(t, toolNames["mtix_briefing"], "mtix_briefing must be registered")
}

// briefingMockStore extends mcpMockStore with configurable node returns
// for briefing tool tests.
type briefingMockStore struct {
	mcpMockStore
	nodes []*model.Node
}

func (b *briefingMockStore) ListNodes(_ context.Context, _ store.NodeFilter, opts store.ListOptions) ([]*model.Node, int, error) {
	if len(b.nodes) == 0 {
		return nil, 0, nil
	}
	n := b.nodes
	if opts.Limit > 0 && opts.Limit < len(n) {
		n = n[:opts.Limit]
	}
	return n, len(b.nodes), nil
}
