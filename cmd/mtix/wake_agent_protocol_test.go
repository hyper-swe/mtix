// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Protocol tests verify actual process diagnostics and quiet coalescing.

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInboxLeaseProcess_HeldAndStale_PrintsExactDiagnostic(t *testing.T) {
	f := newWakeScriptFixture(t)
	var lease struct {
		Holder    string `json:"holder"`
		ExpiresAt string `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent", "--json"), &lease))
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{"held", []string{"acquire", "--agent", "TEST-agent"}, "lease for TEST-agent held by another run until " + lease.ExpiresAt + "; marked dirty, the holder re-checks before release\n"},
		{"stale", []string{"release", "--agent", "TEST-agent", "--holder", strings.Repeat("0", 32)}, "not the holder of this lease; it expired and was taken over or released\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), f.binary, append([]string{"inbox", "lease"}, tt.args...)...)
			cmd.Dir, cmd.Env = f.project, f.env
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			require.Error(t, cmd.Run())
			assert.Equal(t, 6, cmd.ProcessState.ExitCode())
			assert.Empty(t, out.String())
			assert.Equal(t, tt.expected, diagnostic.String())
		})
	}
}

func TestWakeAgentScript_HeldLease_ExitsQuietly(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
	p := f.start(t)
	require.NoError(t, p.wait(t))
	assert.Empty(t, p.output.String())
}
