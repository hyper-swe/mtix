// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_OpenAPIYAML_ReturnsSpec verifies GET /api/openapi.yaml per FR-16.4.
func TestServer_OpenAPIYAML_ReturnsSpec(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Must be YAML content type.
	ct := w.Header().Get("Content-Type")
	assert.True(t, strings.Contains(ct, "yaml") || strings.Contains(ct, "text/plain"),
		"Content-Type should indicate YAML, got: %s", ct)

	// Must contain OpenAPI version identifier.
	body := w.Body.String()
	assert.Contains(t, body, "openapi:")
	assert.Contains(t, body, "3.1")
}

// TestServer_OpenAPIYAML_NoCsrfRequired verifies spec endpoint doesn't require X-Requested-With per FR-16.4.
func TestServer_OpenAPIYAML_NoCsrfRequired(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	// Deliberately no X-Requested-With header.
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.Router().ServeHTTP(w, req)

	// Must succeed without CSRF header (GET is read-only).
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestServer_OpenAPIYAML_ContainsNodeEndpoints verifies spec documents core endpoints.
func TestServer_OpenAPIYAML_ContainsNodeEndpoints(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.Router().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	// Check for key endpoint paths per FR-7.3.
	assert.Contains(t, body, "/api/v1/nodes")
	assert.Contains(t, body, "/api/v1/nodes/{id}")
	assert.Contains(t, body, "/health")
}

// TestServer_OpenAPIYAML_ContainsSchemaComponents verifies reusable schemas per FR-16.3.
func TestServer_OpenAPIYAML_ContainsSchemaComponents(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.Router().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	// Check for required schema components per FR-16.3.
	assert.Contains(t, body, "Node:")
	assert.Contains(t, body, "NodeList:")
	assert.Contains(t, body, "ErrorResponse:")
	assert.Contains(t, body, "CreateNodeRequest:")
}

// TestServer_OpenAPIYAML_ContainsSpecHash verifies X-Spec-Hash header per FR-16.8.
func TestServer_OpenAPIYAML_ContainsSpecHash(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.Router().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// Must include X-Spec-Hash header per FR-16.8.
	hash := w.Header().Get("X-Spec-Hash")
	assert.NotEmpty(t, hash, "X-Spec-Hash header must be present")
	assert.Len(t, hash, 64, "X-Spec-Hash should be SHA-256 hex (64 chars)")
}

// TestServer_OpenAPIJSON_ReturnsSpec verifies optional GET /api/openapi.json per FR-16.4.
func TestServer_OpenAPIJSON_ReturnsSpec(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil)

	s.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	ct := w.Header().Get("Content-Type")
	assert.Contains(t, ct, "json")
}
