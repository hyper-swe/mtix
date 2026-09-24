// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestServer_HostHeader_AcceptsOnlyAllowlistedHosts verifies the Host
// allowlist (MTIX-95.14): localhost and loopback IPs on any port are
// always accepted, a non-loopback bind also accepts exactly its bind
// host, and every other Host is refused with 403 on every route.
func TestServer_HostHeader_AcceptsOnlyAllowlistedHosts(t *testing.T) {
	tests := []struct {
		name   string
		bind   string
		host   string
		accept bool
	}{
		{"loopback bind: ipv4 loopback", "127.0.0.1", "127.0.0.1:8377", true},
		{"loopback bind: localhost other port", "127.0.0.1", "localhost:6849", true},
		{"loopback bind: localhost without port", "127.0.0.1", "localhost", true},
		{"loopback bind: localhost in capitals", "127.0.0.1", "LOCALHOST:8377", true},
		{"loopback bind: ipv6 loopback", "127.0.0.1", "[::1]:8377", true},
		{"loopback bind: ipv6 loopback without port", "127.0.0.1", "[::1]", true},
		{"loopback bind: other loopback address", "127.0.0.1", "127.0.0.2:9000", true},
		{"loopback bind: other name", "127.0.0.1", "example.com", false},
		{"loopback bind: localhost as a subdomain label", "127.0.0.1", "localhost.example.net:8377", false},
		{"loopback bind: loopback ip as a subdomain label", "127.0.0.1", "127.0.0.1.example.net", false},
		{"loopback bind: network address", "127.0.0.1", "192.0.2.10:8377", false},
		{"loopback bind: unspecified address", "127.0.0.1", "0.0.0.0:8377", false},
		{"loopback bind: empty host", "127.0.0.1", "", false},
		{"ipv6 loopback bind: localhost", "::1", "localhost:8377", true},
		{"ipv6 loopback bind: other name", "::1", "other.example", false},
		{"network bind: its own address", "192.0.2.10", "192.0.2.10:8377", true},
		{"network bind: its own address without port", "192.0.2.10", "192.0.2.10", true},
		{"network bind: localhost", "192.0.2.10", "localhost:8377", true},
		{"network bind: ipv6 loopback", "192.0.2.10", "[::1]:8377", true},
		{"network bind: neighbouring address", "192.0.2.10", "192.0.2.11:8377", false},
		{"network bind: other name", "192.0.2.10", "other.example", false},
		{"network bind: empty host", "192.0.2.10", "", false},
		{"hostname bind: its own name", "mtix.example.net", "mtix.example.net:8377", true},
		{"hostname bind: its own name in capitals", "mtix.example.net", "MTIX.example.net:8377", true},
		{"hostname bind: other name", "mtix.example.net", "other.example.net:8377", false},
		{"ipv6 network bind: its own address", "fd00::10", "[fd00::10]:8377", true},
		{"ipv6 network bind: neighbouring address", "fd00::10", "[fd00::11]:8377", false},
		{"wildcard bind: localhost", "0.0.0.0", "localhost:8377", true},
		{"wildcard bind: network address", "0.0.0.0", "192.0.2.10:8377", false},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServerWith(t, logger, ServerConfig{Bind: tt.bind, Port: "0"})

			for _, path := range []string{"/health", "/api/v1/nodes", "/ws/events", "/api/openapi.json"} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Host = tt.host
				s.Router().ServeHTTP(w, req)

				if tt.accept {
					assert.NotEqual(t, http.StatusForbidden, w.Code, path)
					continue
				}
				assert.Equal(t, http.StatusForbidden, w.Code, path)
				assert.Contains(t, w.Body.String(), "HOST_NOT_ALLOWED", path)
			}
		})
	}
}

// TestHostAllowlistMiddleware_EmptyBind_AcceptsLoopbackOnly verifies that
// with no bind host the allowlist is the loopback names alone, and that
// an empty Host is refused even then (MTIX-95.14).
func TestHostAllowlistMiddleware_EmptyBind_AcceptsLoopbackOnly(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		accept bool
	}{
		{"localhost", "localhost:8377", true},
		{"ipv6 loopback", "[::1]:8377", true},
		{"other name", "other.example", false},
		{"empty host", "", false},
	}
	router := setupTestRouter()
	router.Use(HostAllowlistMiddleware(""))
	router.GET("/test", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Host = tt.host
			router.ServeHTTP(w, req)

			if tt.accept {
				assert.Equal(t, http.StatusOK, w.Code)
				return
			}
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Contains(t, w.Body.String(), "HOST_NOT_ALLOWED")
		})
	}
}
