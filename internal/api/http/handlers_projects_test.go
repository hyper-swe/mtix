// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectsOf extracts the set of project prefixes from a /search response.
func projectsOf(t *testing.T, body []byte) map[string]int {
	t.Helper()
	var resp struct {
		Nodes []struct {
			Project string `json:"project"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	out := map[string]int{}
	for _, n := range resp.Nodes {
		out[n.Project]++
	}
	return out
}

// TestAPI_Search_ProjectScopes verifies ?project=<prefix> restricts results to
// that single project per FR-MULTI-PROJECT MP-9.
func TestAPI_Search_ProjectScopes(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Alpha one", "ALPHA")
	createTestNode(t, s, "Alpha two", "ALPHA")
	createTestNode(t, s, "Beta one", "BETA")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodGet, "/api/v1/search?project=ALPHA", "")
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	got := projectsOf(t, w.Body.Bytes())
	assert.Equal(t, 2, got["ALPHA"])
	assert.Equal(t, 0, got["BETA"])
}

// TestAPI_Search_ProjectAll verifies ?project=all spans every project per MP-9.
func TestAPI_Search_ProjectAll(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Alpha one", "ALPHA")
	createTestNode(t, s, "Beta one", "BETA")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodGet, "/api/v1/search?project=all", "")
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	got := projectsOf(t, w.Body.Bytes())
	assert.Equal(t, 1, got["ALPHA"])
	assert.Equal(t, 1, got["BETA"])
}

// TestAPI_Search_OmittedDefaultsToPrimary verifies an omitted project param
// defaults to the configured primary project per MP-9. The default config
// prefix is PROJ.
func TestAPI_Search_OmittedDefaultsToPrimary(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Primary one", "PROJ")
	createTestNode(t, s, "Other one", "OTHER")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodGet, "/api/v1/search", "")
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	got := projectsOf(t, w.Body.Bytes())
	assert.Equal(t, 1, got["PROJ"])
	assert.Equal(t, 0, got["OTHER"])
}

// TestAPI_GetProjects_Contract verifies GET /api/v1/projects returns the
// contract array [{prefix, count, isPrimary}] with isPrimary set on the
// configured primary project per MP-10.
func TestAPI_GetProjects_Contract(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "Primary one", "PROJ")
	createTestNode(t, s, "Primary two", "PROJ")
	createTestNode(t, s, "Other one", "OTHER")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodGet, "/api/v1/projects", "")
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var projects []struct {
		Prefix    string `json:"prefix"`
		Count     int    `json:"count"`
		IsPrimary bool   `json:"isPrimary"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &projects))

	byPrefix := map[string]struct {
		Count     int
		IsPrimary bool
	}{}
	for _, p := range projects {
		byPrefix[p.Prefix] = struct {
			Count     int
			IsPrimary bool
		}{p.Count, p.IsPrimary}
	}

	require.Contains(t, byPrefix, "PROJ")
	require.Contains(t, byPrefix, "OTHER")
	assert.Equal(t, 2, byPrefix["PROJ"].Count)
	assert.True(t, byPrefix["PROJ"].IsPrimary, "PROJ is the configured primary")
	assert.Equal(t, 1, byPrefix["OTHER"].Count)
	assert.False(t, byPrefix["OTHER"].IsPrimary, "OTHER is not primary")
}

// TestAPI_GetProjects_ReturnsJSONArray verifies the response is a JSON array
// (not an object) so the UI can consume it directly per MP-10.
func TestAPI_GetProjects_ReturnsJSONArray(t *testing.T) {
	s := testServer(t)
	createTestNode(t, s, "One", "PROJ")

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodGet, "/api/v1/projects", "")
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var arr []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &arr),
		"response body must be a JSON array")
	require.Len(t, arr, 1)
	assert.Contains(t, arr[0], "prefix")
	assert.Contains(t, arr[0], "count")
	assert.Contains(t, arr[0], "isPrimary")
}

// TestAPI_CreateNode_DefaultsProjectToPrimary verifies POST /nodes without an
// explicit project defaults to the configured primary per MP-11.
func TestAPI_CreateNode_DefaultsProjectToPrimary(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes", `{"title":"No project given"}`)
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var node map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &node))
	assert.Equal(t, "PROJ", node["project"])
}

// TestAPI_CreateNode_HonorsExplicitProject verifies POST /nodes creates into a
// new project directly when one is supplied, with no server-side prompt per
// MP-11.
func TestAPI_CreateNode_HonorsExplicitProject(t *testing.T) {
	s := testServer(t)

	w := httptest.NewRecorder()
	req := apiRequest(http.MethodPost, "/api/v1/nodes", `{"title":"New project node","project":"FRESH"}`)
	s.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var node map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &node))
	assert.Equal(t, "FRESH", node["project"])
}
