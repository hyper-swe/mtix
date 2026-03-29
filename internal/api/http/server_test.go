// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// testServer creates a Server with real store and services for testing.
func testServer(t *testing.T) *Server {
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

	return NewServer(
		st,
		service.NewNodeService(st, broadcaster, config, logger, clock),
		service.NewBackgroundService(st, config, logger, clock),
		service.NewSessionService(st, config, logger, clock),
		service.NewAgentService(st, broadcaster, config, logger, clock),
		configSvc,
		logger,
		ServerConfig{Bind: "127.0.0.1", Port: "0", RateLimit: 0},
		clock,
	)
}

// TestServer_Health_ReturnsOK verifies /health endpoint.
func TestServer_Health_ReturnsOK(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
	assert.Contains(t, resp, "uptime_seconds")
}

// TestServer_CreateNode_201 verifies POST /api/v1/nodes returns 201.
func TestServer_CreateNode_201(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()

	body := `{"title":"API Test","project":"TEST"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var node map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &node)
	require.NoError(t, err)
	assert.Contains(t, node, "id")
	assert.Equal(t, "API Test", node["title"])
	assert.Equal(t, "open", node["status"])
}

// TestServer_GetNode_200 verifies GET /api/v1/nodes/:id returns 200.
func TestServer_GetNode_200(t *testing.T) {
	s := testServer(t)

	// Create a node first.
	createW := httptest.NewRecorder()
	createBody := `{"title":"Get Test","project":"TEST"}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Requested-With", "mtix")
	s.Router().ServeHTTP(createW, createReq)
	require.Equal(t, http.StatusCreated, createW.Code)

	var created map[string]any
	require.NoError(t, json.Unmarshal(createW.Body.Bytes(), &created))
	nodeID := created["id"].(string)

	// Get the node.
	getW := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+nodeID, nil)
	s.Router().ServeHTTP(getW, getReq)

	assert.Equal(t, http.StatusOK, getW.Code)

	var node map[string]any
	require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &node))
	assert.Equal(t, nodeID, node["id"])
	assert.Equal(t, "Get Test", node["title"])
}

// TestServer_GetNode_NotFound_404 verifies 404 for missing node.
func TestServer_GetNode_NotFound_404(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/NONEXISTENT-999", nil)

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "NOT_FOUND", resp.Error.Code)
}

// TestServer_RequestIDHeader verifies X-Request-ID is set on responses.
func TestServer_RequestIDHeader(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	s.Router().ServeHTTP(w, req)

	reqID := w.Header().Get("X-Request-ID")
	assert.NotEmpty(t, reqID, "response should include X-Request-ID")
}

// TestServer_RequestIDHeader_Preserved verifies client-provided ID is preserved.
func TestServer_RequestIDHeader_Preserved(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", "custom-req-123")

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, "custom-req-123", w.Header().Get("X-Request-ID"))
}

// TestServer_CacheControl_Headers verifies Cache-Control: no-store per NFR-5.6.
func TestServer_CacheControl_Headers(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
}

// TestServer_CSRF_GET_Allowed verifies GET requests bypass CSRF check.
func TestServer_CSRF_GET_Allowed(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search", nil)

	s.Router().ServeHTTP(w, req)

	// Should not be 403.
	assert.NotEqual(t, http.StatusForbidden, w.Code)
}

// TestServer_CSRF_POST_WithoutHeader_403 verifies POST without CSRF header is rejected.
func TestServer_CSRF_POST_WithoutHeader_403(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	body := `{"title":"CSRF Test","project":"TEST"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately NOT setting X-Requested-With.

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errObj := resp["error"].(map[string]any)
	assert.Equal(t, "CSRF_VIOLATION", errObj["code"])
}

// TestServer_CSRF_POST_WithHeader_Allowed verifies POST with CSRF header passes.
func TestServer_CSRF_POST_WithHeader_Allowed(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	body := `{"title":"CSRF Pass","project":"TEST"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")

	s.Router().ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusForbidden, w.Code)
}

// TestServer_CORS_Headers verifies CORS headers are set.
func TestServer_CORS_Headers(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/nodes", nil)

	s.Router().ServeHTTP(w, req)

	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "GET")
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "X-Requested-With")
}

// TestServer_ErrorResponse_Schema verifies error response format per FR-7.7.
func TestServer_ErrorResponse_Schema(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	// Get a nonexistent node to trigger error.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/NONEXISTENT", nil)

	s.Router().ServeHTTP(w, req)

	var resp ErrorResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Error.Code)
	assert.NotEmpty(t, resp.Error.Message)
}

// TestServer_DefaultConfig verifies default bind and port.
func TestServer_DefaultConfig(t *testing.T) {
	s := testServer(t)
	assert.Equal(t, "127.0.0.1", s.config.Bind)
}
