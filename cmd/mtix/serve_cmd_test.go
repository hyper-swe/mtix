// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewServeCmd_Help_NamesOnlyStartedServers verifies that the serve
// help text names the HTTP and WebSocket server that serve starts and no
// server it does not start (MTIX-95.14).
func TestNewServeCmd_Help_NamesOnlyStartedServers(t *testing.T) {
	cmd := newServeCmd()

	assert.Equal(t, "Start the mtix HTTP and WebSocket server", cmd.Short)

	for _, text := range []string{cmd.Short, cmd.Long, cmd.Example, cmd.Flags().FlagUsages()} {
		assert.NotContains(t, strings.ToLower(text), "grpc")
	}
}

// TestServeConfig_BuildVersion_ReachesServer verifies that serve passes
// the build version injected at link time to the HTTP server, which
// reports it from /health (FR-7.3b, MTIX-95.14).
func TestServeConfig_BuildVersion_ReachesServer(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{"release build", "0.5.4-beta"},
		{"development build", "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := version
			version = tt.version
			t.Cleanup(func() { version = old })

			cfg := serveConfig("192.0.2.10", 8377)

			assert.Equal(t, tt.version, cfg.Version)
			assert.Equal(t, "192.0.2.10", cfg.Bind)
			assert.Equal(t, "8377", cfg.Port)
		})
	}
}
