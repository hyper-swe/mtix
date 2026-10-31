// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownPortOriginCases lists browser origins sent to a server bound to
// 127.0.0.1:8377. Only the server's own origin, written with the bind
// host or a loopback alias on the configured port, is accepted; a page
// served by any other local web server is refused.
func ownPortOriginCases() []originCase {
	return []originCase{
		{"own port: ipv4 loopback", "http://127.0.0.1:8377", true},
		{"own port: localhost", "http://localhost:8377", true},
		{"own port: ipv6 loopback", "http://[::1]:8377", true},
		{"other port: ipv4 loopback", "http://127.0.0.1:9999", false},
		{"other port: localhost", "http://localhost:9999", false},
		{"other port: ipv6 loopback", "http://[::1]:9999", false},
		{"other port: web dev server", "http://localhost:5173", false},
		{"port omitted means 80", "http://localhost", false},
		{"https on own port", "https://127.0.0.1:8377", false},
		{"other loopback address on own port", "http://127.0.0.2:8377", false},
	}
}

// ownPortTestServer returns a server bound to 127.0.0.1:8377.
func ownPortTestServer(t *testing.T) *serverFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return testServerWith(t, logger, ServerConfig{Bind: "127.0.0.1", Port: "8377"})
}

// TestServer_OtherPortOrigin_MutationRefused verifies that a mutating
// request with the CSRF header from another local port is refused
// through the full middleware stack, and the own origin still passes.
func TestServer_OtherPortOrigin_MutationRefused(t *testing.T) {
	s := ownPortTestServer(t)
	for _, tt := range ownPortOriginCases() {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes",
				strings.NewReader(`{"title":"Origin check","project":"TEST"}`))
			req.Host = "127.0.0.1:8377"
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			s.Router().ServeHTTP(w, req)

			if !tt.accept {
				assert.Equal(t, http.StatusForbidden, w.Code)
				assert.Contains(t, w.Body.String(), "ORIGIN_NOT_ALLOWED")
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
				return
			}
			assert.Equal(t, http.StatusCreated, w.Code)
			assert.Equal(t, tt.origin, w.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

// TestServer_OtherPortOrigin_PreflightRefused verifies that a CORS
// preflight from another local port is refused through the full
// middleware stack, and the own origin's preflight still passes.
func TestServer_OtherPortOrigin_PreflightRefused(t *testing.T) {
	s := ownPortTestServer(t)
	for _, tt := range ownPortOriginCases() {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodOptions, "/api/v1/nodes", nil)
			req.Host = "127.0.0.1:8377"
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			req.Header.Set("Access-Control-Request-Headers", "content-type, x-requested-with")
			s.Router().ServeHTTP(w, req)

			if !tt.accept {
				assert.Equal(t, http.StatusForbidden, w.Code)
				assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
				return
			}
			assert.Equal(t, http.StatusNoContent, w.Code)
			assert.Equal(t, tt.origin, w.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

// TestHandleWebSocket_OtherPortOrigin_UpgradeRefused verifies that the
// WebSocket upgrade's own origin check refuses another local port. The
// handler is mounted without CORSMiddleware so the upgrade's check is the
// one under test.
func TestHandleWebSocket_OtherPortOrigin_UpgradeRefused(t *testing.T) {
	s := ownPortTestServer(t)
	router := gin.New()
	router.GET("/ws/events", s.handleWebSocket)
	ts := httptest.NewServer(router)
	t.Cleanup(func() {
		ts.Close()
		s.wsHub.Close()
	})
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"

	for _, tt := range ownPortOriginCases() {
		t.Run(tt.name, func(t *testing.T) {
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {tt.origin}})
			if resp != nil && resp.Body != nil {
				t.Cleanup(func() { _ = resp.Body.Close() })
			}
			if !tt.accept {
				require.Error(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, http.StatusForbidden, resp.StatusCode)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
			_ = conn.Close()
		})
	}
}

// TestServer_AnyResponse_NoAllowCredentials verifies that no response
// carries Access-Control-Allow-Credentials, whether the origin is
// accepted or refused, and whatever the method.
func TestServer_AnyResponse_NoAllowCredentials(t *testing.T) {
	s := ownPortTestServer(t)
	origins := []string{"", "http://127.0.0.1:8377", "http://localhost:8377", "http://localhost:9999", "https://other.example"}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodOptions}
	for _, origin := range origins {
		for _, method := range methods {
			t.Run(method+" "+origin, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/api/v1/nodes", strings.NewReader(`{"title":"Credentials","project":"TEST"}`))
				req.Host = "127.0.0.1:8377"
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Requested-With", "mtix")
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
				if method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
				}
				s.Router().ServeHTTP(w, req)

				assert.Empty(t, w.Header().Values("Access-Control-Allow-Credentials"))
			})
		}
	}
}
