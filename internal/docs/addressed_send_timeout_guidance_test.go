// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-20.8: every shipped sync runbook describes the same CLI timeout and
// recovery behavior as the long-lived executors.
func TestAddressedSendTimeoutGuidance_AllSyncSurfaces(t *testing.T) {
	paths := []string{
		"templates/skills/sync.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(body), "addressed-send push (the detached CLI child, MCP, or `mtix serve`) that reaches its 5 minute limit is recorded as `timeout` and releases the push lock")
		})
	}
}
