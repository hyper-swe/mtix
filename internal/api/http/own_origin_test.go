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

// bindOriginCase is one Origin sent to a server bound to bind:port.
type bindOriginCase struct {
	name   string
	bind   string
	port   string
	origin string
	accept bool
}

// bindOriginCases lists how the bind address changes the origin rule
// (MTIX-95.14): a server bound to a specific non-loopback address also
// accepts exactly its own origin (http, the bind host, the configured
// port); loopback and wildcard binds accept local origins only.
func bindOriginCases() []bindOriginCase {
	return []bindOriginCase{
		{"network bind: own origin", "192.0.2.10", "8377", "http://192.0.2.10:8377", true},
		{"network bind: local origin", "192.0.2.10", "8377", "http://localhost:3000", true},
		{"network bind: another port", "192.0.2.10", "8377", "http://192.0.2.10:8378", false},
		{"network bind: default port", "192.0.2.10", "8377", "http://192.0.2.10", false},
		{"network bind: neighbouring address", "192.0.2.10", "8377", "http://192.0.2.11:8377", false},
		{"network bind: https", "192.0.2.10", "8377", "https://192.0.2.10:8377", false},
		{"network bind: address as a subdomain label", "192.0.2.10", "8377", "http://192.0.2.10.example.net:8377", false},
		{"network bind: user info", "192.0.2.10", "8377", "http://user@192.0.2.10:8377", false},
		{"network bind: path", "192.0.2.10", "8377", "http://192.0.2.10:8377/app", false},
		{"network bind: other site", "192.0.2.10", "8377", "http://other.example:8377", false},
		{"port 80 bind: own origin without port", "192.0.2.10", "80", "http://192.0.2.10", true},
		{"port 80 bind: own origin with port", "192.0.2.10", "80", "http://192.0.2.10:80", true},
		{"port 80 bind: another port", "192.0.2.10", "80", "http://192.0.2.10:8377", false},
		{"hostname bind: own origin", "mtix.example.net", "8377", "http://mtix.example.net:8377", true},
		{"hostname bind: own origin in capitals", "mtix.example.net", "8377", "http://MTIX.example.net:8377", true},
		{"hostname bind: name as a subdomain label", "mtix.example.net", "8377", "http://mtix.example.net.other.example:8377", false},
		{"ipv6 network bind: own origin", "fd00::10", "8377", "http://[fd00::10]:8377", true},
		{"ipv6 network bind: neighbouring address", "fd00::10", "8377", "http://[fd00::11]:8377", false},
		{"ipv4 wildcard bind: its own address", "0.0.0.0", "8377", "http://0.0.0.0:8377", false},
		{"ipv4 wildcard bind: network address", "0.0.0.0", "8377", "http://192.0.2.10:8377", false},
		{"ipv4 wildcard bind: local origin", "0.0.0.0", "8377", "http://localhost:8377", true},
		{"ipv6 wildcard bind: its own address", "::", "8377", "http://[::]:8377", false},
		{"loopback bind: network address", "127.0.0.1", "8377", "http://192.0.2.10:8377", false},
		{"loopback bind: own origin", "127.0.0.1", "8377", "http://127.0.0.1:8377", true},
		{"no bind: empty host", "", "8377", "http://:8377", false},
	}
}

// TestCORSMiddleware_NetworkBind_AcceptsOwnOriginOnly verifies that
// CORSMiddleware applies the bind-dependent part of the origin rule
// (FR-9.1, MTIX-95.14).
func TestCORSMiddleware_NetworkBind_AcceptsOwnOriginOnly(t *testing.T) {
	for _, tt := range bindOriginCases() {
		t.Run(tt.name, func(t *testing.T) {
			router := corsTestRouter(tt.bind, tt.port)
			for _, method := range []string{http.MethodPost, http.MethodOptions} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/test", nil)
				req.Header.Set("X-Requested-With", "mtix")
				req.Header.Set("Origin", tt.origin)
				router.ServeHTTP(w, req)

				if !tt.accept {
					assert.Equal(t, http.StatusForbidden, w.Code, method)
					assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"), method)
					continue
				}
				assert.NotEqual(t, http.StatusForbidden, w.Code, method)
				assert.Equal(t, tt.origin, w.Header().Get("Access-Control-Allow-Origin"), method)
			}
		})
	}
}

// TestHandleWebSocket_NetworkBind_AcceptsOwnOriginOnly verifies that the
// WebSocket upgrade applies the same bind-dependent origin rule as
// CORSMiddleware (FR-7.5, MTIX-95.14).
func TestHandleWebSocket_NetworkBind_AcceptsOwnOriginOnly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range bindOriginCases() {
		t.Run(tt.name, func(t *testing.T) {
			s := testServerWith(t, logger, ServerConfig{Bind: tt.bind, Port: tt.port})
			router := gin.New()
			router.GET("/ws/events", s.handleWebSocket)
			ts := httptest.NewServer(router)
			t.Cleanup(func() {
				ts.Close()
				s.wsHub.Close()
			})
			wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"

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

// TestServer_NetworkBind_AcceptsOwnOriginThroughStack verifies that the
// server's middleware stack gives CORSMiddleware its bind address and
// port: the web UI's own origin passes on a network bind, and the same
// address on another port is refused (FR-9.1, MTIX-95.14).
func TestServer_NetworkBind_AcceptsOwnOriginThroughStack(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		accept bool
	}{
		{"own origin", "http://192.0.2.10:8377", true},
		{"another port", "http://192.0.2.10:8378", false},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "8377"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", strings.NewReader(`{"title":"Own origin","project":"TEST"}`))
			req.Host = "192.0.2.10:8377"
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "mtix")
			s.Router().ServeHTTP(w, req)

			if !tt.accept {
				assert.Equal(t, http.StatusForbidden, w.Code)
				assert.Contains(t, w.Body.String(), "ORIGIN_NOT_ALLOWED")
				return
			}
			assert.Equal(t, http.StatusCreated, w.Code)
			assert.Equal(t, tt.origin, w.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}
