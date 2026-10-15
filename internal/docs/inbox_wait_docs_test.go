// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Unbounded-wait guidance ships with the CLI and MCP feature (FR-19, Commandment 15).
package docs

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInboxWaitDocs_TimeoutZeroAndExitCodes_AcrossGuidance(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl",
		"internal/docs/templates/skills/multi_agent.md.tmpl",
		".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md",
		"USERMANUAL.md", "docs/CLI_REFERENCE.md",
	} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, phrase := range []string{"--timeout 0", "exit 0", "exit 5", "exit 7", "SIGINT", "SIGTERM"} {
				assert.True(t, strings.Contains(body, phrase), "%s missing %q", path, phrase)
			}
		})
	}
}

func TestInboxWaitDocs_MCPExplicitZeroAndOmittedDefault_InSkills(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/skills/multi_agent.md.tmpl",
		".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md",
		"USERMANUAL.md",
	} {
		body := readAgentInstruction(t, filepath.Join("..", "..", path))
		assert.True(t, strings.Contains(body, "timeout_seconds=0"), "%s missing MCP explicit-zero guidance", path)
		assert.True(t, strings.Contains(body, "default 60"), "%s missing MCP omitted-timeout default", path)
		assert.True(t, strings.Contains(body, "negative"), "%s missing negative-timeout guidance", path)
	}
	changelog := readAgentInstruction(t, filepath.Join("..", "..", "CHANGELOG.md"))
	require.True(t, strings.Contains(changelog, "--timeout 0"), "changelog missing zero-timeout entry")
}
