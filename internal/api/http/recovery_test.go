// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestServer_PanicRecovery_LogsNoRequestHeaders verifies that a handler
// panic becomes a 500 response and that the recovery log carries the
// panic, method and path but no request header name or value
// (MTIX-95.14). Everything the process could log is captured: the server
// logger and gin's default writers.
func TestServer_PanicRecovery_LogsNoRequestHeaders(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"plain panic", "handler failed"},
		{"broken connection", &net.OpError{Op: "write", Net: "tcp",
			Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}}},
		{"reset connection", &net.OpError{Op: "read", Net: "tcp",
			Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}},
		{"aborted handler", http.ErrAbortHandler},
	}
	headers := map[string]string{
		"Cookie":        "session=cookie-value-7f3a",
		"Authorization": "Bearer token-value-7f3a",
		"X-Api-Key":     "key-value-7f3a",
		"User-Agent":    "agent-value-7f3a",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			oldErr, oldOut := gin.DefaultErrorWriter, gin.DefaultWriter
			gin.DefaultErrorWriter, gin.DefaultWriter = &buf, &buf
			t.Cleanup(func() { gin.DefaultErrorWriter, gin.DefaultWriter = oldErr, oldOut })

			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			s := testServerWith(t, logger, ServerConfig{Bind: "127.0.0.1", Port: "0"})
			s.Router().GET("/panics", func(_ *gin.Context) { panic(tt.value) })

			w := httptest.NewRecorder()
			req := newLocalRequest(http.MethodGet, "/panics", nil)
			for name, value := range headers {
				req.Header.Set(name, value)
			}
			s.Router().ServeHTTP(w, req)

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			logged := buf.String()
			assert.Contains(t, logged, "panic recovered")
			assert.Contains(t, logged, "path=/panics")
			for name, value := range headers {
				assert.NotContains(t, logged, value, "header %s value logged", name)
				assert.NotContains(t, logged, name+":", "header %s logged", name)
			}
		})
	}
}
