// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// originCase is one browser Origin and whether the server accepts it.
type originCase struct {
	name   string
	origin string
	accept bool
}

// originCases lists the Origin values that CORSMiddleware and the
// WebSocket upgrade must treat alike (MTIX-95.14): http or https with a
// host that is exactly localhost or a loopback IP, any port; an empty
// Origin (a non-browser client) is accepted.
func originCases() []originCase {
	return []originCase{
		{"no origin header", "", true},
		{"localhost with port", "http://localhost:9999", true},
		{"localhost without port", "http://localhost", true},
		{"localhost over https", "https://localhost:3000", true},
		{"localhost in capitals", "http://LOCALHOST:3000", true},
		{"ipv4 loopback", "http://127.0.0.1:8377", true},
		{"other ipv4 loopback address", "http://127.0.0.2:8080", true},
		{"ipv6 loopback", "http://[::1]:8377", true},
		{"ipv6 loopback over https", "https://[::1]", true},
		{"localhost as a subdomain label", "http://localhost.example.net", false},
		{"loopback ip as a subdomain label", "http://127.0.0.1.example.net", false},
		{"host that starts with localhost", "http://localhostx.example:8080", false},
		{"other site over https", "https://other.example", false},
		{"other site over http", "http://other.example:8377", false},
		{"unspecified address", "http://0.0.0.0:8377", false},
		{"network address", "http://192.0.2.1:8377", false},
		{"opaque origin", "null", false},
		{"file scheme", "file://", false},
		{"ftp scheme", "ftp://localhost", false},
		{"websocket scheme", "ws://localhost:8377", false},
		{"user info", "http://user@localhost:3000", false},
		{"path", "http://localhost:3000/app", false},
		{"query", "http://localhost:3000?x=1", false},
		{"fragment", "http://localhost:3000#x", false},
		{"no host", "http://", false},
		{"invalid port", "http://localhost:abc", false},
	}
}

// corsTestRouter mounts CORSMiddleware in front of GET and POST handlers.
func corsTestRouter() *gin.Engine {
	router := setupTestRouter()
	router.Use(CORSMiddleware())
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"reached": true}) }
	router.GET("/test", ok)
	router.POST("/test", ok)
	return router
}

// TestCORSMiddleware_Origin_AcceptsOnlyLocalOrigins verifies that
// CORSMiddleware grants cross-origin access to local origins only and
// refuses every other Origin with 403, preflight included (FR-9.1,
// MTIX-95.14).
func TestCORSMiddleware_Origin_AcceptsOnlyLocalOrigins(t *testing.T) {
	router := corsTestRouter()
	for _, tt := range originCases() {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
			t.Run(tt.name+"/"+method, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/test", nil)
				req.Header.Set("X-Requested-With", "mtix")
				if method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
					req.Header.Set("Access-Control-Request-Headers", "x-requested-with")
				}
				if tt.origin != "" {
					req.Header.Set("Origin", tt.origin)
				}
				router.ServeHTTP(w, req)

				if !tt.accept {
					assert.Equal(t, http.StatusForbidden, w.Code)
					assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
					assert.NotContains(t, w.Body.String(), "reached")
					assert.Contains(t, w.Body.String(), "ORIGIN_NOT_ALLOWED")
					return
				}
				if method == http.MethodOptions {
					assert.Equal(t, http.StatusNoContent, w.Code)
				} else {
					assert.Equal(t, http.StatusOK, w.Code)
					assert.Contains(t, w.Body.String(), "reached")
				}
				if tt.origin == "" {
					assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
					return
				}
				assert.Equal(t, tt.origin, w.Header().Get("Access-Control-Allow-Origin"))
				assert.Equal(t, "Origin", w.Header().Get("Vary"))
			})
		}
	}
}

// TestHandleWebSocket_Origin_AcceptsOnlyLocalOrigins verifies that the
// WebSocket upgrade applies the same origin rule as CORSMiddleware
// (FR-7.5, MTIX-95.14). The handler is mounted without CORSMiddleware so
// the upgrade's own check is the one under test.
func TestHandleWebSocket_Origin_AcceptsOnlyLocalOrigins(t *testing.T) {
	s := testServer(t)
	router := setupTestRouter()
	router.GET("/ws/events", s.handleWebSocket)
	ts := httptest.NewServer(router)
	t.Cleanup(func() {
		ts.Close()
		s.wsHub.Close()
	})
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"

	for _, tt := range originCases() {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.origin != "" {
				header.Set("Origin", tt.origin)
			}
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
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
