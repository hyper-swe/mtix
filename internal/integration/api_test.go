// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package integration contains end-to-end integration tests per MTIX-11.1.
// REST API integration tests verify HTTP endpoints against real store and services.
// These tests will be enabled once the REST API server (MTIX-5.x) is implemented.
package integration

import (
	"context"
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

// apiTestEnv holds a test environment for REST API integration tests.
type apiTestEnv struct {
	store      *sqlite.Store
	nodeSvc    *service.NodeService
	bgSvc      *service.BackgroundService
	sessionSvc *service.SessionService
	agentSvc   *service.AgentService
	configSvc  *service.ConfigService
}

// setupAPIEnv creates services backed by a real SQLite store for API testing.
func setupAPIEnv(t *testing.T) *apiTestEnv {
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

	return &apiTestEnv{
		store:      st,
		nodeSvc:    service.NewNodeService(st, broadcaster, config, logger, clock),
		bgSvc:      service.NewBackgroundService(st, config, logger, clock),
		sessionSvc: service.NewSessionService(st, config, logger, clock),
		agentSvc:   service.NewAgentService(st, broadcaster, config, logger, clock),
		configSvc:  configSvc,
	}
}

// stubHandler creates a minimal HTTP handler for testing service integration.
// This simulates the REST API handler pattern until the real API is built.
func stubHandler(env *apiTestEnv) http.Handler {
	mux := http.NewServeMux()

	// POST /api/v1/nodes — create a node.
	mux.HandleFunc("POST /api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Title    string `json:"title"`
			Project  string `json:"project"`
			ParentID string `json:"parent_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.Project == "" {
			req.Project = "TEST"
		}

		node, err := env.nodeSvc.CreateNode(r.Context(), &service.CreateNodeRequest{
			Title:    req.Title,
			Project:  req.Project,
			ParentID: req.ParentID,
			Creator:  "api",
		})
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(node)
	})

	// GET /api/v1/nodes/{id} — get a node.
	mux.HandleFunc("GET /api/v1/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		node, err := env.nodeSvc.GetNode(r.Context(), nodeID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(node)
	})

	return mux
}

// TestAPI_CreateNode_201 verifies POST /api/v1/nodes returns 201.
func TestAPI_CreateNode_201(t *testing.T) {
	env := setupAPIEnv(t)
	ts := httptest.NewServer(stubHandler(env))
	defer ts.Close()

	body := `{"title":"API Test Node","project":"TEST"}`
	resp, err := http.Post(ts.URL+"/api/v1/nodes", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var node map[string]any
	err = json.NewDecoder(resp.Body).Decode(&node)
	require.NoError(t, err)

	assert.Contains(t, node, "id")
	assert.Equal(t, "API Test Node", node["title"])
	assert.Equal(t, "open", node["status"])
}

// TestAPI_GetNode_200 verifies GET /api/v1/nodes/{id} returns 200.
func TestAPI_GetNode_200(t *testing.T) {
	env := setupAPIEnv(t)
	ts := httptest.NewServer(stubHandler(env))
	defer ts.Close()

	// Create a node via service.
	ctx := context.Background()
	node, err := env.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "Get Test",
		Project: "TEST",
		Creator: "test",
	})
	require.NoError(t, err)

	// GET the node.
	resp, err := http.Get(ts.URL + "/api/v1/nodes/" + node.ID)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	assert.Equal(t, node.ID, result["id"])
	assert.Equal(t, "Get Test", result["title"])
}

// TestAPI_NonexistentNode_404 verifies GET for missing node returns 404.
func TestAPI_NonexistentNode_404(t *testing.T) {
	env := setupAPIEnv(t)
	ts := httptest.NewServer(stubHandler(env))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/nodes/NONEXISTENT-999")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestAPI_CreateNode_InvalidBody_400 verifies malformed request returns 400.
func TestAPI_CreateNode_InvalidBody_400(t *testing.T) {
	env := setupAPIEnv(t)
	ts := httptest.NewServer(stubHandler(env))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/nodes", "application/json", strings.NewReader("{invalid}"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestAPI_ContentType_JSON verifies responses have correct Content-Type.
func TestAPI_ContentType_JSON(t *testing.T) {
	env := setupAPIEnv(t)
	ts := httptest.NewServer(stubHandler(env))
	defer ts.Close()

	body := `{"title":"Content Type Test","project":"TEST"}`
	resp, err := http.Post(ts.URL+"/api/v1/nodes", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

// TestAPI_CreateAndRetrieve_RoundTrip verifies create→get data integrity.
func TestAPI_CreateAndRetrieve_RoundTrip(t *testing.T) {
	env := setupAPIEnv(t)
	ts := httptest.NewServer(stubHandler(env))
	defer ts.Close()

	// Create.
	createBody := `{"title":"Round Trip Test","project":"RT"}`
	createResp, err := http.Post(ts.URL+"/api/v1/nodes", "application/json", strings.NewReader(createBody))
	require.NoError(t, err)
	defer func() { _ = createResp.Body.Close() }()

	var created map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	nodeID := created["id"].(string)

	// Retrieve.
	getResp, err := http.Get(ts.URL + "/api/v1/nodes/" + nodeID)
	require.NoError(t, err)
	defer func() { _ = getResp.Body.Close() }()

	var fetched map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&fetched))

	assert.Equal(t, nodeID, fetched["id"])
	assert.Equal(t, "Round Trip Test", fetched["title"])
	assert.Equal(t, "open", fetched["status"])
}
