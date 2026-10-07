// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent identity instructions explain accepted assignment boundaries (MTIX-125).
package mtix_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentID_Docs_ExplainRawValidationAndEmptyAssignment(t *testing.T) {
	for _, path := range []string{"USERMANUAL.md", "docs/SKILL.md", ".codex-plugin/skills/planning/SKILL.md", ".codex-plugin/skills/task-execution/SKILL.md", ".codex-plugin/skills/multi-agent/SKILL.md", "internal/docs/templates/skill.md.tmpl", "internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl", "internal/docs/templates/skills/planning.md.tmpl", "internal/docs/templates/skills/task_execution.md.tmpl", "internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-planning.md", ".claude-plugin/skills/mtix-task-execution.md", ".claude-plugin/skills/mtix-multi-agent.md", "docs/AGENTS.md", "docs/codex/AGENTS.md", "docs/CLI_REFERENCE.md"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			s := string(raw)
			for _, phrase := range []string{"64 UTF-8 bytes", "whitespace-only", "control", "format", "raw", "empty", "clear"} {
				require.Contains(t, s, phrase)
			}
		})
	}
}
