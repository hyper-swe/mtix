// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent-facing docs teach that every action item becomes a ticket (MTIX-209).
package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionItemGuidance_TeachesTicketPerActionItem(t *testing.T) {
	paths := []string{
		"templates/claude.md.tmpl",
		"templates/agents.md.tmpl",
		"templates/skills/task_execution.md.tmpl",
		"../../.claude-plugin/skills/mtix-task-execution.md",
		"../../.codex-plugin/skills/task-execution/SKILL.md",
		"../../docs/AGENTS.md",
		"../../README.md",
		"../../USERMANUAL.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, fragment := range []string{
				"Every action item becomes a ticket",
				"--under",
				"--description",
				"--prompt",
				"--acceptance",
			} {
				assert.Contains(t, string(raw), fragment)
			}
		})
	}
}
