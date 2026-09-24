// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_Health_ReportsBuildVersion verifies that /health reports the
// build version injected through ServerConfig, and "dev" when none is
// given (FR-7.3b, MTIX-95.14).
func TestServer_Health_ReportsBuildVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"injected release version", "0.5.4-beta", "0.5.4-beta"},
		{"injected development version", "0.6.0-dev.3", "0.6.0-dev.3"},
		{"no version", "", "dev"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServerWith(t, logger, ServerConfig{Bind: "127.0.0.1", Port: "0", Version: tt.version})

			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, newLocalRequest(http.MethodGet, "/health", nil))

			require.Equal(t, http.StatusOK, w.Code)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.want, resp["version"])
		})
	}
}

// TestServer_Start_BindAddress_WarnsOnlyWhenNotLoopback verifies that
// Start warns about network exposure only for a bind address that is not
// loopback, with ::1 and the rest of 127.0.0.0/8 treated as loopback
// (NFR-5.2, MTIX-95.14). The port is invalid, so Start returns right after
// the warning decision without listening; Start also logs the version.
func TestServer_Start_BindAddress_WarnsOnlyWhenNotLoopback(t *testing.T) {
	tests := []struct {
		name string
		bind string
		warn bool
	}{
		{"ipv4 loopback", "127.0.0.1", false},
		{"other ipv4 loopback address", "127.0.0.2", false},
		{"localhost", "localhost", false},
		{"ipv6 loopback", "::1", false},
		{"ipv4 wildcard", "0.0.0.0", true},
		{"ipv6 wildcard", "::", true},
		{"network address", "192.0.2.10", true},
		{"host name", "mtix.example.net", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs, warnings bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			s := testServerWith(t, logger, ServerConfig{Bind: tt.bind, Port: "-1", Version: "0.5.4-beta"})
			s.warnOut = &warnings

			require.Error(t, s.Start(), "an invalid port must fail to listen")

			if tt.warn {
				assert.Contains(t, warnings.String(), "WARNING: Binding to")
			} else {
				assert.Empty(t, warnings.String())
			}
			assert.Contains(t, logs.String(), "version=0.5.4-beta")
		})
	}
}
