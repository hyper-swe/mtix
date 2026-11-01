// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

// These tests prove NFR-5.1 defense-in-depth on API and embedded SPA responses.
import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/web"
)

// expectedCSP is the exact policy; equality (not containment) makes any added,
// duplicated or reordered directive fail (MTIX-187.7.9).
const expectedCSP = "default-src 'none'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' https:; font-src 'self'; " +
	"connect-src 'self'; object-src 'none'; frame-src 'none'; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'"

func assertCSP(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, []string{expectedCSP}, w.Header().Values("Content-Security-Policy"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

func TestSecurityHeaders_CSPPresent(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{"/health", "/api/v1/nodes/missing"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, newLocalRequest(http.MethodGet, path, nil))
			assertCSP(t, w)
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
		})
	}
}

func TestSPA_ServedWithCSP(t *testing.T) {
	if !web.HasEmbeddedUI() {
		t.Skip("noui build excludes SPA")
	}
	s := testServer(t)
	for _, path := range []string{"/", "/ticket/TEST-1"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, newLocalRequest(http.MethodGet, path, nil))
			// Security headers only: the status of the /ticket/<id> fallback is
			// a separate routing concern (MTIX-210) and must not be pinned here.
			assertCSP(t, w)
		})
	}
}
