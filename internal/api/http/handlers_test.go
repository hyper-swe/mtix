// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestNode is a helper that creates a node and returns its ID.
func createTestNode(t *testing.T, s *Server, title, project string) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"title":"` + title + `","project":"` + project + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var node map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &node))
	return node["id"].(string)
}

// apiRequest is a helper to make an API request with standard headers.
func apiRequest(method, path, body string) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	return req
}

// --- MTIX-5.2.1: Node CRUD ---

// TestAPI_UpdateNode_Returns200 verifies PATCH /api/v1/nodes/:id.
func TestAPI_UpdateNode_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Update Test", "TEST")

	w := httptest.NewRecorder()
	body := `{"title":"Updated Title"}`
	req := apiRequest(http.MethodPatch, "/api/v1/nodes/"+nodeID, body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var node map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &node))
	assert.Equal(t, "Updated Title", node["title"])
}

// TestAPI_DeleteNode_Returns200 verifies DELETE /api/v1/nodes/:id.
func TestAPI_DeleteNode_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Delete Test", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodDelete, "/api/v1/nodes/"+nodeID, "")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["deleted"])
}

// TestAPI_DeleteNode_NoCascade verifies cascade=false default.
func TestAPI_DeleteNode_NoCascade(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "No Cascade", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodDelete, "/api/v1/nodes/"+nodeID+"?cascade=false", "")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["cascade"])
}

// TestAPI_GetActivity_Returns200 verifies GET /api/v1/nodes/:id/activity per FR-3.6.
func TestAPI_GetActivity_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Activity Test Node", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+nodeID+"/activity", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	entries, ok := resp["entries"].([]any)
	require.True(t, ok, "response should contain entries array")
	assert.GreaterOrEqual(t, len(entries), 1, "should have at least a 'created' entry")
}

// TestAPI_GetActivity_NonExistent_Returns404 verifies 404 for missing node.
func TestAPI_GetActivity_NonExistent_Returns404(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/NONEXISTENT/activity", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestAPI_GetActivity_WithPagination verifies limit/offset query params.
func TestAPI_GetActivity_WithPagination(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Paginated Activity", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+nodeID+"/activity?limit=1&offset=0", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	entries := resp["entries"].([]any)
	assert.Len(t, entries, 1)
}

// TestAPI_GetChildren_Returns200 verifies GET /api/v1/nodes/:id/children.
func TestAPI_GetChildren_Returns200(t *testing.T) {
	s := testServer(t)
	parentID := createTestNode(t, s, "Parent", "TEST")

	// Create child under parent.
	w := httptest.NewRecorder()
	body := `{"title":"Child","project":"TEST","parent_id":"` + parentID + `"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes", body)
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	// Get children.
	cw := httptest.NewRecorder()
	cReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+parentID+"/children", nil)
	s.Router().ServeHTTP(cw, cReq)

	assert.Equal(t, http.StatusOK, cw.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["total"])
}

// TestAPI_DecomposeNode_Returns201 verifies POST /api/v1/nodes/:id/decompose.
func TestAPI_DecomposeNode_Returns201(t *testing.T) {
	s := testServer(t)
	parentID := createTestNode(t, s, "Decompose Parent", "TEST")

	w := httptest.NewRecorder()
	body := `{"children":[{"title":"Sub1"},{"title":"Sub2"}]}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+parentID+"/decompose", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["total"])
}

// --- MTIX-5.2.2: Workflow ---

// TestAPI_Claim_Returns200 verifies POST /api/v1/nodes/:id/claim.
func TestAPI_Claim_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Claim Test", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", "{}")
	req.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "claimed", resp["status"])
	assert.Equal(t, "agent-1", resp["agent"])
}

// TestAPI_Claim_MissingAgent_Returns400 verifies claim without agent fails.
func TestAPI_Claim_MissingAgent_Returns400(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "No Agent Claim", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", "{}")
	// No X-Agent-ID header
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestAPI_Unclaim_MissingReason_Returns400 verifies unclaim without reason.
func TestAPI_Unclaim_MissingReason_Returns400(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Unclaim Test", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/unclaim", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestAPI_Done_Returns200 verifies POST /api/v1/nodes/:id/done.
func TestAPI_Done_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Done Test", "TEST")

	// First transition from open to in_progress via claim.
	cw := httptest.NewRecorder()
	cReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", "{}")
	cReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(cw, cReq)
	require.Equal(t, http.StatusOK, cw.Code)

	// Now transition to done from in_progress.
	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/done", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "done", resp["status"])
}

// TestAPI_Cancel_WithCascade verifies cancel with cascade.
func TestAPI_Cancel_WithCascade(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Cancel Test", "TEST")

	w := httptest.NewRecorder()
	body := `{"reason":"obsolete","cascade":true}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/cancel", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "cancelled", resp["status"])
	assert.Equal(t, "obsolete", resp["reason"])
}

// TestAPI_Cancel_MissingReason_Returns400 verifies cancel without reason.
func TestAPI_Cancel_MissingReason_Returns400(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Cancel No Reason", "TEST")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/cancel", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestAPI_Reopen_Returns200 verifies POST /api/v1/nodes/:id/reopen.
func TestAPI_Reopen_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Reopen Test", "TEST")

	// First claim to transition to in_progress.
	cw := httptest.NewRecorder()
	cReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", "{}")
	cReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(cw, cReq)
	require.Equal(t, http.StatusOK, cw.Code)

	// Then mark as done.
	dw := httptest.NewRecorder()
	dReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/done", `{}`)
	s.Router().ServeHTTP(dw, dReq)
	require.Equal(t, http.StatusOK, dw.Code)

	// Then reopen.
	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/reopen", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "open", resp["status"])
}

// TestAPI_Defer_WithUntil verifies defer with until parameter.
func TestAPI_Defer_WithUntil(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Defer Test", "TEST")

	w := httptest.NewRecorder()
	body := `{"until":"2026-04-01T00:00:00Z"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/defer", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "deferred", resp["status"])
}

// TestAPI_Defer_InvalidUntil_Returns400 verifies invalid timestamp.
func TestAPI_Defer_InvalidUntil_Returns400(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Bad Defer", "TEST")

	w := httptest.NewRecorder()
	body := `{"until":"not-a-timestamp"}`
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/defer", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestAPI_Rerun_Returns200 verifies POST /api/v1/nodes/:id/rerun.
func TestAPI_Rerun_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Rerun Test", "TEST")

	// Claim to transition to in_progress first.
	cw := httptest.NewRecorder()
	cReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/claim", "{}")
	cReq.Header.Set("X-Agent-ID", "agent-1")
	s.Router().ServeHTTP(cw, cReq)
	require.Equal(t, http.StatusOK, cw.Code)

	// Mark as done.
	dw := httptest.NewRecorder()
	dReq := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/done", `{}`)
	s.Router().ServeHTTP(dw, dReq)
	require.Equal(t, http.StatusOK, dw.Code)

	// Rerun.
	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/rerun", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "open", resp["status"])
	assert.Equal(t, true, resp["rerun"])
}

// --- MTIX-5.2.3: Query ---

// TestAPI_ListNodes_WithFilters verifies GET /api/v1/search with filters.
func TestAPI_ListNodes_WithFilters(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Filter Test 1", "TEST")
	createTestNode(t, s, "Filter Test 2", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?status=open&limit=10", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.GreaterOrEqual(t, resp["total"].(float64), float64(2))
	assert.Contains(t, resp, "has_more")
}

// TestAPI_Ready_Returns200 verifies GET /api/v1/ready.
func TestAPI_Ready_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "nodes")
	assert.Contains(t, resp, "total")
}

// TestAPI_Blocked_Returns200 verifies GET /api/v1/blocked.
func TestAPI_Blocked_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/blocked", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestAPI_Orphans_Returns200 verifies GET /api/v1/orphans.
func TestAPI_Orphans_Returns200(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Orphan Test", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orphans", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "nodes")
}

// TestAPI_Stats_Returns200 verifies GET /api/v1/stats.
func TestAPI_Stats_Returns200(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Stats Test", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "total")
	assert.Contains(t, resp, "counts")
}

// TestAPI_Progress_Returns200 verifies GET /api/v1/progress/:id.
func TestAPI_Progress_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Progress Test", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/progress/"+nodeID, nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "progress")
	assert.Contains(t, resp, "invalidated_count")
}

// TestAPI_Ancestors_Returns200 verifies GET /api/v1/nodes/:id/ancestors.
func TestAPI_Ancestors_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Ancestors Test", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+nodeID+"/ancestors", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "ancestors")
}

// --- MTIX-5.2.4: Dependencies ---

// TestAPI_AddDependency_Returns201 verifies POST /api/v1/deps.
func TestAPI_AddDependency_Returns201(t *testing.T) {
	s := testServer(t)
	id1 := createTestNode(t, s, "Dep From", "TEST")
	id2 := createTestNode(t, s, "Dep To", "TEST")

	w := httptest.NewRecorder()
	body := `{"from_id":"` + id1 + `","to_id":"` + id2 + `","dep_type":"related"}`
	req := apiRequest(http.MethodPost, "/api/v1/deps", body)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
}

// TestAPI_GetDependencies_Returns200 verifies GET /api/v1/deps/:id.
func TestAPI_GetDependencies_Returns200(t *testing.T) {
	s := testServer(t)
	nodeID := createTestNode(t, s, "Dep Query", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/deps/"+nodeID, nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "blockers")
}

// TestAPI_RemoveDependency_MissingType_Returns400 verifies dep_type required.
func TestAPI_RemoveDependency_MissingType_Returns400(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodDelete, "/api/v1/deps/A/B", "")
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- MTIX-5.2.5: Agent/Session ---

// TestAPI_AgentHeartbeat_Returns200 verifies heartbeat endpoint.
func TestAPI_AgentHeartbeat_Returns200(t *testing.T) {
	s := testServer(t)

	// FR-10.1a: Agent must exist before heartbeat (no phantom creation).
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.store.WriteDB().ExecContext(ctx,
		`INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat) VALUES (?, ?, 'idle', ?, ?)`,
		"agent-1", "TEST", now, now,
	)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/agents/agent-1/heartbeat", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

// --- MTIX-5.2.7: Admin ---

// TestAPI_GetConfig_Returns200 verifies GET /api/v1/admin/config.
func TestAPI_GetConfig_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "auto_claim")
	assert.Contains(t, resp, "max_recommended_depth")
}

// TestAPI_Verify_Returns200 verifies POST /api/v1/admin/verify.
func TestAPI_Verify_Returns200(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/admin/verify", `{}`)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["integrity_check"])
}

// --- MTIX-5.2.10: Pagination ---

// TestAPI_Pagination_HasMore verifies pagination metadata.
func TestAPI_Pagination_HasMore(t *testing.T) {
	s := testServer(t)
	// Create 3 nodes.
	createTestNode(t, s, "Page 1", "TEST")
	createTestNode(t, s, "Page 2", "TEST")
	createTestNode(t, s, "Page 3", "TEST")

	// Request with limit=2.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?limit=2&offset=0", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["limit"])
	assert.Equal(t, float64(0), resp["offset"])
	assert.Equal(t, true, resp["has_more"])
}

// TestAPI_Pagination_MaxLimit verifies limit is clamped to 500.
func TestAPI_Pagination_MaxLimit(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?limit=1000", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(500), resp["limit"])
}

// TestAPI_Pagination_OffsetBeyondTotal_ReturnsEmpty verifies offset beyond total.
func TestAPI_Pagination_OffsetBeyondTotal_ReturnsEmpty(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "One Node", "TEST")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?offset=999", nil)
	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	nodes := resp["nodes"]
	// Should return empty array or nil, not error.
	if nodes != nil {
		nodesList := nodes.([]any)
		assert.Empty(t, nodesList)
	}
}
