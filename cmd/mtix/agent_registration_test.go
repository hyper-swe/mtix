// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Registration command tests cover repeat notices, JSON, and discoverable help.
package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAgentRegister_Repeat_SucceedsWithNotice(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			initTestApp(t)
			app.jsonOutput = jsonOutput
			first := captureStdout(t, func() { require.NoError(t, runAgentRegister("test-worker")) })
			repeated := captureStdout(t, func() { require.NoError(t, runAgentRegister("test-worker")) })
			if jsonOutput {
				for _, tc := range []struct{ out, status string }{{first, "registered"}, {repeated, "already_registered"}} {
					var got map[string]string
					require.NoError(t, json.Unmarshal([]byte(tc.out), &got))
					assert.Equal(t, "test-worker", got["agent_id"])
					assert.Equal(t, tc.status, got["status"])
				}
			} else {
				assert.Contains(t, first, "Registered agent test-worker")
				assert.Contains(t, repeated, "already registered")
			}
		})
	}
}

func TestAgentRegisterHelp_Repeat_ExplainsHeartbeatAndPreservation(t *testing.T) {
	cmd := newAgentRegisterCmd()
	assert.Contains(t, cmd.Long, "already registered")
	assert.Contains(t, cmd.Long, "heartbeat")
	assert.Contains(t, cmd.Long, "session")
}
