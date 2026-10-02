// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// forwardedHeaderCases lists request headers that name a client address
// other than the TCP peer. The server uses the TCP peer (MTIX-95.14).
func forwardedHeaderCases() []struct {
	name   string
	header string
	values [2]string
} {
	return []struct {
		name   string
		header string
		values [2]string
	}{
		{"X-Forwarded-For", "X-Forwarded-For", [2]string{"10.0.0.5", "10.0.0.6"}},
		{"X-Forwarded-For chain", "X-Forwarded-For", [2]string{"10.0.0.5, 10.0.0.9", "10.0.0.6, 10.0.0.9"}},
		{"X-Real-IP", "X-Real-IP", [2]string{"10.0.0.5", "10.0.0.6"}},
		{"X-Agent-ID", "X-Agent-ID", [2]string{"agent-a", "agent-b"}},
	}
}

// TestServer_RequestLog_ForwardedHeaders_LogsTCPPeer verifies that the
// request log records the TCP peer as client_ip whatever forwarded
// headers the request carries (NFR-4.2, MTIX-95.14).
func TestServer_RequestLog_ForwardedHeaders_LogsTCPPeer(t *testing.T) {
	for _, tt := range forwardedHeaderCases() {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			s := testServerWith(t, logger, ServerConfig{Bind: "127.0.0.1", Port: "0"})

			w := httptest.NewRecorder()
			req := newLocalRequest(http.MethodGet, "/health", nil)
			req.RemoteAddr = "203.0.113.7:4711"
			req.Header.Set(tt.header, tt.values[0])
			s.Router().ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, buf.String(), "client_ip=203.0.113.7")
			assert.NotContains(t, buf.String(), "10.0.0.5")
		})
	}
}

// TestServer_RateLimit_ForwardedHeaders_KeysOnTCPPeer verifies that the
// server's rate limiter counts requests per TCP peer: a second request
// from the same peer is limited whatever headers it carries, and another
// peer has its own budget (NFR-1.5, MTIX-95.14).
func TestServer_RateLimit_ForwardedHeaders_KeysOnTCPPeer(t *testing.T) {
	for _, tt := range forwardedHeaderCases() {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			s := testServerWith(t, logger, ServerConfig{Bind: "127.0.0.1", Port: "0", RateLimit: 1})

			send := func(remoteAddr, value string) int {
				w := httptest.NewRecorder()
				req := newLocalRequest(http.MethodGet, "/health", nil)
				req.RemoteAddr = remoteAddr
				req.Header.Set(tt.header, value)
				s.Router().ServeHTTP(w, req)
				return w.Code
			}

			assert.Equal(t, http.StatusOK, send("203.0.113.7:4711", tt.values[0]))
			assert.Equal(t, http.StatusTooManyRequests, send("203.0.113.7:4712", tt.values[1]),
				"same peer, different header value")
			assert.Equal(t, http.StatusOK, send("203.0.113.8:4711", tt.values[0]),
				"another peer has its own budget")
		})
	}
}
