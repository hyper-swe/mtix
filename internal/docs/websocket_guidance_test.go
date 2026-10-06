// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebSocketGuidance_SubtreeAndEventFilters pins the FR-7.5a filtering
// contract across generated agent instructions, skills, mirrors and manual.
func TestWebSocketGuidance_SubtreeAndEventFilters(t *testing.T) {
	const guidance = "WebSocket subscriptions at `/ws/events` match `under` to the named node and its dotted descendants on a `.` boundary: `PROJ-1` includes `PROJ-1` and `PROJ-1.2`, but rejects `PROJ-10` and `PROJ-10.2`. Omitted or empty `under` accepts every node. A non-empty `events` whitelist must also match; an omitted or empty `events` list accepts every event type."
	paths := []string{
		"templates/agents.md.tmpl",
		"templates/claude.md.tmpl",
		"templates/skill.md.tmpl",
		"../../docs/SKILL.md",
		"templates/skills/task_execution.md.tmpl",
		"../../.claude-plugin/skills/mtix-task-execution.md",
		"../../.codex-plugin/skills/task-execution/SKILL.md",
		"../../USERMANUAL.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			normalized := strings.Join(strings.Fields(string(content)), " ")
			assert.Contains(t, normalized, guidance, "complete WebSocket filtering guidance")
		})
	}
}
