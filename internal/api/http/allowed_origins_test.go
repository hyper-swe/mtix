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

// allowListOrigin is the extra origin the allow-list tests configure.
const allowListOrigin = "http://localhost:5173"

// allowListOriginCases lists browser origins sent to a server bound to
// 127.0.0.1:8377 whose allow-list names http://localhost:5173. The own
// origin and the configured origin are accepted; a near-miss of the
// configured origin and every other origin are refused.
func allowListOriginCases() []originCase {
	return []originCase{
		{"empty origin", "", true},
		{"own port: ipv4 loopback", "http://127.0.0.1:8377", true},
		{"own port: localhost", "http://localhost:8377", true},
		{"own port: ipv6 loopback", "http://[::1]:8377", true},
		{"other port: ipv4 loopback", "http://127.0.0.1:9999", false},
		{"other port: localhost", "http://localhost:9999", false},
		{"other port: ipv6 loopback", "http://[::1]:9999", false},
		{"port omitted means 80", "http://localhost", false},
		{"https on own port", "https://127.0.0.1:8377", false},
		{"configured origin", allowListOrigin, true},
		{"configured origin in capitals", "http://LOCALHOST:5173", true},
		{"configured origin on another port", "http://localhost:5174", false},
		{"configured origin with trailing slash", "http://localhost:5173/", false},
		{"configured origin over https", "https://localhost:5173", false},
		{"configured port with another loopback alias", "http://127.0.0.1:5173", false},
		{"opaque origin", "null", false},
		{"non-loopback host", "http://192.0.2.1:8377", false},
		{"other site", "https://other.example", false},
	}
}

// allowListTestServer returns a server bound to 127.0.0.1:8377 with
// allowListOrigin in its allow-list.
func allowListTestServer(t *testing.T) *serverFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return testServerWith(t, logger, ServerConfig{
		Bind: "127.0.0.1", Port: "8377", AllowedOrigins: []string{allowListOrigin},
	})
}

// TestServer_AllowedOrigins_AcceptsOwnAndConfiguredOnly verifies the
// allow-list through the full middleware stack for a mutating request
// with the CSRF header and for a CORS preflight.
func TestServer_AllowedOrigins_AcceptsOwnAndConfiguredOnly(t *testing.T) {
	s := allowListTestServer(t)
	for _, tt := range allowListOriginCases() {
		for _, method := range []string{http.MethodPost, http.MethodOptions} {
			t.Run(tt.name+"/"+method, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/api/v1/nodes",
					strings.NewReader(`{"title":"Allow-list","project":"TEST"}`))
				req.Host = "127.0.0.1:8377"
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Requested-With", "mtix")
				req.Header.Set("Access-Control-Request-Method", http.MethodPost)
				if tt.origin != "" {
					req.Header.Set("Origin", tt.origin)
				}
				s.Router().ServeHTTP(w, req)

				assert.Empty(t, w.Header().Values("Access-Control-Allow-Credentials"))
				if !tt.accept {
					assert.Equal(t, http.StatusForbidden, w.Code)
					assert.Contains(t, w.Body.String(), "ORIGIN_NOT_ALLOWED")
					assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
					return
				}
				want := http.StatusCreated
				if method == http.MethodOptions {
					want = http.StatusNoContent
				}
				assert.Equal(t, want, w.Code)
				assert.Equal(t, tt.origin, w.Header().Get("Access-Control-Allow-Origin"))
			})
		}
	}
}

// TestHandleWebSocket_AllowedOrigins_AcceptsOwnAndConfiguredOnly verifies
// that the WebSocket upgrade's own check applies the same allow-list. The
// handler is mounted without CORSMiddleware.
func TestHandleWebSocket_AllowedOrigins_AcceptsOwnAndConfiguredOnly(t *testing.T) {
	s := allowListTestServer(t)
	router := gin.New()
	router.GET("/ws/events", s.handleWebSocket)
	ts := httptest.NewServer(router)
	t.Cleanup(func() {
		ts.Close()
		s.wsHub.Close()
	})
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"

	for _, tt := range allowListOriginCases() {
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

// TestNewServer_AllowedOrigins_CopiesCallerSlice verifies that changing
// the caller's slice after NewServer does not widen the allow-list.
func TestNewServer_AllowedOrigins_CopiesCallerSlice(t *testing.T) {
	origins := []string{allowListOrigin}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "127.0.0.1", Port: "8377", AllowedOrigins: origins})
	origins[0] = "https://other.example"

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Host = "127.0.0.1:8377"
	req.Header.Set("Origin", "https://other.example")
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestValidateAllowedOrigins_EntryForm_RefusesWhatNoBrowserSends verifies
// that an allow-list entry must be written exactly as a browser sends an
// origin.
func TestValidateAllowedOrigins_EntryForm_RefusesWhatNoBrowserSends(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		valid  bool
	}{
		{"http with port", "http://localhost:5173", true},
		{"https with port", "https://app.example:8443", true},
		{"https without port", "https://app.example", true},
		{"ipv6 in brackets", "http://[::1]:5173", true},
		{"trailing slash", "http://localhost:5173/", false},
		{"path", "http://localhost:5173/app", false},
		{"query", "http://localhost:5173?x=1", false},
		{"fragment", "http://localhost:5173#x", false},
		{"user info", "http://user@localhost:5173", false},
		{"ipv6 without brackets", "http://::1:5173", false},
		{"no host", "http://", false},
		{"no scheme", "localhost:5173", false},
		{"websocket scheme", "ws://localhost:5173", false},
		{"wildcard", "*", false},
		{"opaque origin", "null", false},
		{"empty entry", "", false},
		{"http default port", "http://localhost:80", false},
		{"https default port", "https://app.example:443", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAllowedOrigins([]string{tt.origin})
			if tt.valid {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "allowed origin")
		})
	}
}

// TestServerStart_InvalidAllowedOrigin_Refused verifies that a library
// caller cannot start a server with an allow-list entry that no browser
// could send.
func TestServerStart_InvalidAllowedOrigin_Refused(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := testServerWith(t, logger, ServerConfig{
		Bind: "127.0.0.1", Port: "0", AllowedOrigins: []string{"http://localhost:5173/"},
	})
	err := s.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed origin")
}
