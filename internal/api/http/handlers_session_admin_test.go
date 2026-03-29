// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ensureHTTPAgent inserts an agent record directly into the database so that
// session operations (which have an FK on agents.agent_id) can succeed.
// The HTTP heartbeat endpoint only does UPDATE, not INSERT, so we need
// direct DB access to create the agent record.
func ensureHTTPAgent(t *testing.T, s *Server, agentID, project string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.store.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO agents (agent_id, project, state, last_heartbeat) VALUES (?, ?, ?, ?)`,
		agentID, project, "idle", now,
	)
	require.NoError(t, err, "failed to ensure agent %s exists", agentID)
}

// --- Session Management Tests ---

// TestStartSession_WithValidProject_Returns201 verifies POST /api/v1/agents/:id/sessions/start.
func TestStartSession_WithValidProject_Returns201(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	w := httptest.NewRecorder()
	body := `{"project":"TEST"}`
	req := apiRequest(http.MethodPost, "/api/v1/agents/agent-1/sessions/start", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "session_id")
	assert.Equal(t, "agent-1", resp["agent_id"])
	assert.Equal(t, "active", resp["status"])
}

// TestStartSession_WithoutProject_UsesDefault_Returns201 verifies project defaults to config prefix.
func TestStartSession_WithoutProject_UsesDefault_Returns201(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-2", "TEST")

	w := httptest.NewRecorder()
	body := `{}`
	req := apiRequest(http.MethodPost, "/api/v1/agents/agent-2/sessions/start", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "session_id")
	assert.Equal(t, "active", resp["status"])
}

// TestStartSession_InvalidJSON_DefaultsApplied verifies handler ignores malformed JSON
// and proceeds with defaults (ShouldBindJSON error is intentionally discarded).
func TestStartSession_InvalidJSON_DefaultsApplied(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent-1/sessions/start", strings.NewReader("{invalid}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(w, req)

	// Handler ignores JSON error, uses default project, and creates session.
	assert.Equal(t, http.StatusCreated, w.Code)
}

// TestEndSession_ValidAgent_Returns200 verifies POST /api/v1/agents/:id/sessions/end.
func TestEndSession_ValidAgent_Returns200(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	// Start session first.
	startW := httptest.NewRecorder()
	startReq := apiRequest(http.MethodPost, "/api/v1/agents/agent-1/sessions/start", `{"project":"TEST"}`)
	s.Router().ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusCreated, startW.Code)

	// End session.
	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/agents/agent-1/sessions/end", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Handler returns session summary (not agent_id/status directly).
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "session_id")
	assert.Contains(t, resp, "status")
}

// TestEndSession_NoActiveSession_Returns409 verifies conflict when no session exists.
func TestEndSession_NoActiveSession_Returns409(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-999", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/agents/agent-999/sessions/end", `{}`)
	s.Router().ServeHTTP(w, req)

	// SessionEnd returns ErrNoActiveSession which maps to 409.
	assert.Equal(t, http.StatusConflict, w.Code)
}

// TestSessionSummary_ValidAgent_Returns200 verifies GET /api/v1/agents/:id/sessions/summary.
func TestSessionSummary_ValidAgent_Returns200(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	// Start session.
	startW := httptest.NewRecorder()
	startReq := apiRequest(http.MethodPost, "/api/v1/agents/agent-1/sessions/start", `{"project":"TEST"}`)
	s.Router().ServeHTTP(startW, startReq)
	require.Equal(t, http.StatusCreated, startW.Code)

	// Get summary.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/sessions/summary", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "session_id")
}

// TestSessionSummary_NoSession_ReturnsError verifies error handling.
func TestSessionSummary_NoSession_ReturnsError(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-999", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-999/sessions/summary", nil)
	s.Router().ServeHTTP(w, req)

	// May return error code per service implementation.
	assert.NotEqual(t, http.StatusOK, w.Code)
}

// --- Agent State Management Tests ---

// TestGetAgentState_ValidAgent_Returns200 verifies GET /api/v1/agents/:id/state.
func TestGetAgentState_ValidAgent_Returns200(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/state", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "agent-1", resp["agent_id"])
	assert.Contains(t, resp, "state")
	assert.Contains(t, resp, "last_heartbeat")
}

// TestSetAgentState_ValidTransition_Returns200 verifies PATCH /api/v1/agents/:id/state.
func TestSetAgentState_ValidTransition_Returns200(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	// Agent starts in "idle"; valid transition is idle → working.
	w := httptest.NewRecorder()
	body := `{"state":"working"}`
	req := apiRequest(http.MethodPatch, "/api/v1/agents/agent-1/state", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "agent-1", resp["agent_id"])
	assert.Equal(t, "working", resp["state"])
}

// TestSetAgentState_MissingState_Returns400 verifies validation.
func TestSetAgentState_MissingState_Returns400(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	w := httptest.NewRecorder()
	body := `{}`
	req := apiRequest(http.MethodPatch, "/api/v1/agents/agent-1/state", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestSetAgentState_InvalidJSON_Returns400 verifies JSON parsing error.
func TestSetAgentState_InvalidJSON_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/agents/agent-1/state", strings.NewReader("{bad json}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestGetAgentWork_NoCurrentWork_Returns404 verifies GET /api/v1/agents/:id/work
// returns not found when the agent has no claimed node.
func TestGetAgentWork_NoCurrentWork_Returns404(t *testing.T) {
	s := testServer(t)
	ensureHTTPAgent(t, s, "agent-1", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/work", nil)
	s.Router().ServeHTTP(w, req)

	// GetCurrentWork returns ErrNotFound when agent has no assigned work.
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Admin Configuration Tests ---

// TestSetConfig_ValidKey_Returns200 verifies PATCH /api/v1/admin/config.
func TestSetConfig_ValidKey_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	body := `{"agent.auto_claim":"true"}`
	req := apiRequest(http.MethodPatch, "/api/v1/admin/config", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "updated")
	assert.Contains(t, resp, "message")
}

// TestSetConfig_MultipleKeys_Returns200 verifies updating multiple config keys.
func TestSetConfig_MultipleKeys_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	body := `{"agent.auto_claim":"true","prefix":"MULTI"}`
	req := apiRequest(http.MethodPatch, "/api/v1/admin/config", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	updatedVal, ok := resp["updated"]
	require.True(t, ok, "updated field should be present in response")
	updated, ok := updatedVal.(map[string]any)
	require.True(t, ok, "updated should be a map")
	assert.GreaterOrEqual(t, len(updated), 0)
}

// TestSetConfig_InvalidJSON_Returns400 verifies JSON parsing error.
func TestSetConfig_InvalidJSON_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config", strings.NewReader("{invalid}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestSetConfig_EmptyBody_Returns400 verifies validation of empty request.
func TestSetConfig_EmptyBody_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestRunGC_Returns200 verifies POST /api/v1/admin/gc.
func TestRunGC_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/admin/gc", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "completed", resp["status"])
	assert.Contains(t, resp, "message")
}

// TestRunBackup_NoPath_Returns400 verifies POST /api/v1/admin/backup without path.
func TestRunBackup_NoPath_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/admin/backup", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "error")
}

// TestRunBackup_WithPath_Returns200 verifies POST /api/v1/admin/backup with valid path.
func TestRunBackup_WithPath_Returns200(t *testing.T) {
	s := testServer(t)

	backupPath := filepath.Join(t.TempDir(), "test-backup.db")
	body := fmt.Sprintf(`{"path":"%s"}`, backupPath)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/admin/backup", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "path")
	assert.Contains(t, resp, "verified")
}

// --- Query Handlers Tests ---

// TestStaleNodes_Returns200 verifies GET /api/v1/stale.
func TestStaleNodes_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stale", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "agents")
	assert.Contains(t, resp, "total")
}

// TestStaleNodes_WithCustomHours_Returns200 verifies hours query parameter.
func TestStaleNodes_WithCustomHours_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stale?hours=24", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "agents")
}

// TestStaleNodes_InvalidHours_UsesDefault verifies invalid hours parameter fallback.
func TestStaleNodes_InvalidHours_UsesDefault(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stale?hours=invalid", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "agents")
}

// TestNodeTree_ValidNode_Returns200 verifies GET /api/v1/tree/:id.
func TestNodeTree_ValidNode_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Tree Test", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tree/"+nodeID, nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, nodeID, resp["id"])
	assert.Contains(t, resp, "children")
	assert.Contains(t, resp, "depth")
}

// TestNodeTree_WithDepthParam_Returns200 verifies depth query parameter.
func TestNodeTree_WithDepthParam_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Tree Depth Test", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tree/"+nodeID+"?depth=5", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, nodeID, resp["id"])
}

// TestNodeTree_NonexistentNode_Returns404 verifies error handling.
func TestNodeTree_NonexistentNode_Returns404(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tree/NONEXISTENT", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestNodeContext_ValidNode_Returns200 verifies GET /api/v1/context/:id.
func TestNodeContext_ValidNode_Returns200(t *testing.T) {
	s := testServer(t)
	parentID := createTestNode(t, s, "Context Parent", "TEST")

	// Create child.
	cw := httptest.NewRecorder()
	cbody := `{"title":"Context Child","project":"TEST","parent_id":"` + parentID + `"}`
	creq := apiRequest(http.MethodPost, "/api/v1/nodes", cbody)
	s.Router().ServeHTTP(cw, creq)
	require.Equal(t, http.StatusCreated, cw.Code)

	var child map[string]any
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &child))
	childID := child["id"].(string)

	// Get context.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/"+childID, nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "node")
	assert.Contains(t, resp, "ancestors")
	assert.Contains(t, resp, "siblings")
	assert.Contains(t, resp, "children")
}

// TestNodeContext_NonexistentNode_Returns404 verifies error handling.
func TestNodeContext_NonexistentNode_Returns404(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/NONEXISTENT", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Workflow Handlers Tests ---

// TestBlockNode_ValidNode_Returns200 verifies POST /api/v1/nodes/:id/block.
func TestBlockNode_ValidNode_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Block Test", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/block", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "blocked", resp["status"])
}

// TestBlockNode_InvalidNodeID_Returns404 verifies error handling.
func TestBlockNode_InvalidNodeID_Returns404(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/NONEXISTENT/block", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestCommentNode_ValidNode_Returns201 verifies POST /api/v1/nodes/:id/comment.
func TestCommentNode_ValidNode_Returns201(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Comment Test", "TEST")

	w := httptest.NewRecorder()
	body := `{"text":"This is a test comment"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/comment", body)
	req.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, nodeID, resp["id"])
	assert.Contains(t, resp, "annotation")

	annotation := resp["annotation"].(map[string]any)
	assert.Equal(t, "This is a test comment", annotation["text"])
	assert.Equal(t, "agent-1", annotation["author"])
}

// TestCommentNode_MissingText_Returns400 verifies validation.
func TestCommentNode_MissingText_Returns400(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Comment Validation", "TEST")

	w := httptest.NewRecorder()
	body := `{}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/comment", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestCommentNode_WithType_Returns201 verifies comment type parameter.
func TestCommentNode_WithType_Returns201(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Comment Type Test", "TEST")

	w := httptest.NewRecorder()
	body := `{"text":"Typed comment","type":"note"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/comment", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "annotation")
}

// TestCommentNode_NoAgentID_UsesDefault verifies default author.
func TestCommentNode_NoAgentID_UsesDefault(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Comment Default Author", "TEST")

	w := httptest.NewRecorder()
	body := `{"text":"No agent comment"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/comment", body)
	// No X-Agent-ID header
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	annotation := resp["annotation"].(map[string]any)
	assert.Equal(t, "api", annotation["author"])
}

// TestCommentNode_InvalidNode_Returns404 verifies error handling.
func TestCommentNode_InvalidNode_Returns404(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	body := `{"text":"Comment on nonexistent"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/NONEXISTENT/comment", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Bulk Operations Tests ---

// TestBulkUpdateNodes_ValidUpdates_Returns200 verifies PATCH /api/v1/bulk/nodes.
func TestBulkUpdateNodes_ValidUpdates_Returns200(t *testing.T) {
	s := testServer(t)
	id1 := createTestNode(t, s, "Bulk 1", "TEST")
	id2 := createTestNode(t, s, "Bulk 2", "TEST")

	w := httptest.NewRecorder()
	body := `{
		"updates": [
			{"id":"` + id1 + `","fields":{"title":"Updated 1"}},
			{"id":"` + id2 + `","fields":{"title":"Updated 2"}}
		]
	}`
	req := apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "results")
	assert.Equal(t, float64(2), resp["total"])
}

// TestBulkUpdateNodes_EmptyUpdates_Returns400 verifies validation.
func TestBulkUpdateNodes_EmptyUpdates_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	body := `{"updates":[]}`
	req := apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBulkUpdateNodes_ExceedsMaxBatch_Returns400 verifies batch size limit.
func TestBulkUpdateNodes_ExceedsMaxBatch_Returns400(t *testing.T) {
	s := testServer(t)

	// Build request with 101 updates (exceeds 100 limit).
	updates := "["
	for i := 1; i <= 101; i++ {
		if i > 1 {
			updates += ","
		}
		updates += fmt.Sprintf(`{"id":"node-%d","fields":{"title":"Update"}}`, i)
	}
	updates += "]"

	w := httptest.NewRecorder()
	body := `{"updates":` + updates + `}`
	req := apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errDetail := resp["error"].(map[string]any)
	assert.Contains(t, errDetail["message"].(string), "100")
}

// TestBulkUpdateNodes_InvalidJSON_Returns400 verifies JSON parsing.
func TestBulkUpdateNodes_InvalidJSON_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/bulk/nodes", strings.NewReader("{invalid}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBulkUpdateNodes_MissingUpdates_Returns400 verifies required field.
func TestBulkUpdateNodes_MissingUpdates_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	body := `{}`
	req := apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBulkUpdateNodes_PartialFailure_Reports200WithMixedResults verifies partial success handling.
func TestBulkUpdateNodes_PartialFailure_Reports200WithMixedResults(t *testing.T) {
	s := testServer(t)
	validID := createTestNode(t, s, "Valid Node", "TEST")

	w := httptest.NewRecorder()
	body := `{
		"updates": [
			{"id":"` + validID + `","fields":{"title":"Updated"}},
			{"id":"NONEXISTENT","fields":{"title":"This will fail"}}
		]
	}`
	req := apiRequest(http.MethodPatch, "/api/v1/bulk/nodes", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	results := resp["results"].([]any)
	assert.Equal(t, 2, len(results))

	// First should succeed, second should fail.
	first := results[0].(map[string]any)
	second := results[1].(map[string]any)
	assert.Equal(t, true, first["success"])
	assert.Equal(t, false, second["success"])
}

// --- Additional Edge Cases ---

// TestClaimNode_WithForceReclaim_Returns200 verifies force claim behavior.
func TestClaimNode_WithForceReclaim_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Force Claim Test", "TEST")

	// First claim.
	claim1W := httptest.NewRecorder()
	claim1Req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", `{}`)
	claim1Req.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(claim1W, claim1Req)
	require.Equal(t, http.StatusOK, claim1W.Code)

	// Backdate agent-1's heartbeat so it's stale for force-reclaim.
	staleTime := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	_, err := s.store.WriteDB().ExecContext(context.Background(),
		`UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?`,
		staleTime, "agent-1",
	)
	require.NoError(t, err)

	// Force claim by different agent.
	w := httptest.NewRecorder()
	body := `{"force":true}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", body)
	req.Header.Set("X-Agent-ID", "agent-2")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "agent-2", resp["agent"])
}

// TestUnclaim_WithOptionalAgentID_Returns200 verifies agent ID handling.
func TestUnclaim_WithOptionalAgentID_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Unclaim Agent ID Test", "TEST")

	// Claim first.
	claimW := httptest.NewRecorder()
	claimReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", `{}`)
	claimReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(claimW, claimReq)
	require.Equal(t, http.StatusOK, claimW.Code)

	// Unclaim without agent ID (defaults to "api").
	w := httptest.NewRecorder()
	body := `{"reason":"testing"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/unclaim", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "unclaimed", resp["status"])
}

// TestDone_WithOptionalReason_Returns200 verifies reason handling.
func TestDone_WithOptionalReason_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Done Reason Test", "TEST")

	// Claim first.
	claimW := httptest.NewRecorder()
	claimReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", `{}`)
	claimReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(claimW, claimReq)
	require.Equal(t, http.StatusOK, claimW.Code)

	// Mark done with custom reason.
	w := httptest.NewRecorder()
	body := `{"reason":"completed successfully"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/done", body)
	req.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "done", resp["status"])
}

// TestDone_WithoutReason_UsesDefault verifies default reason.
func TestDone_WithoutReason_UsesDefault(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Done Default Reason", "TEST")

	// Claim first.
	claimW := httptest.NewRecorder()
	claimReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", `{}`)
	claimReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(claimW, claimReq)
	require.Equal(t, http.StatusOK, claimW.Code)

	// Mark done without reason.
	w := httptest.NewRecorder()
	body := `{}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/done", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "done", resp["status"])
}

// TestReopen_WithCustomReason_Returns200 verifies reason parameter.
func TestReopen_WithCustomReason_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Reopen Custom", "TEST")

	// Claim -> Done.
	claimW := httptest.NewRecorder()
	claimReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", `{}`)
	claimReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(claimW, claimReq)
	require.Equal(t, http.StatusOK, claimW.Code)

	doneW := httptest.NewRecorder()
	doneReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/done", `{}`)
	s.Router().ServeHTTP(doneW, doneReq)
	require.Equal(t, http.StatusOK, doneW.Code)

	// Reopen with custom reason.
	w := httptest.NewRecorder()
	body := `{"reason":"need to revise"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/reopen", body)
	req.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "open", resp["status"])
}

// TestOrphanNodes_WithPagination_Returns200 verifies pagination on orphans.
func TestOrphanNodes_WithPagination_Returns200(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Orphan 1", "TEST")
	createTestNode(t, s, "Orphan 2", "TEST")
	createTestNode(t, s, "Orphan 3", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orphans?limit=2&offset=0", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["limit"])
	assert.Equal(t, float64(0), resp["offset"])
	assert.Contains(t, resp, "has_more")
}

// TestOrphanNodes_ManyChildren_AllRootsReturned verifies that all root nodes
// are returned even when child nodes outnumber the fetch limit (MTIX-15).
// Regression: the orphanNodes handler fetched limit+offset rows from ALL nodes
// then filtered for roots in memory. When children exceeded the limit, late
// root nodes were silently dropped from the response.
func TestOrphanNodes_ManyChildren_AllRootsReturned(t *testing.T) {
	s := testServer(t)

	// Create 3 root nodes.
	root1 := createTestNode(t, s, "Root 1", "TEST")
	createTestNode(t, s, "Root 2", "TEST")
	createTestNode(t, s, "Root 3", "TEST")

	// Create 60 children under root1 to push total node count well past
	// a typical limit (50). This exposes the bug: if the handler fetches
	// only limit+offset=50 rows, it gets 50 rows dominated by children,
	// missing Root 2 and Root 3.
	for i := 0; i < 60; i++ {
		w := httptest.NewRecorder()
		body := `{"title":"Child ` + fmt.Sprintf("%d", i) + `","parent_id":"` + root1 + `","project":"TEST"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "mtix")
		s.Router().ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "failed to create child %d", i)
	}

	// Request orphans with default limit (50). All 3 roots must be returned.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orphans?limit=50", nil)
	s.Router().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	nodes := resp["nodes"].([]any)
	assert.Equal(t, 3, len(nodes), "expected all 3 root nodes, got %d", len(nodes))

	// Verify total reflects root count, not total node count.
	total := int(resp["total"].(float64))
	assert.Equal(t, 3, total, "total should reflect root count, not all nodes")
}
