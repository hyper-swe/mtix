// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent instruction tests keep boot guidance short without losing detailed references.
package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const conciseRegistrationRule = "2. Register with `mtix agent register <agent-id>` at session boot; repeats exit 0 with an \"already registered\" notice. Start a session before work and end it when done"

func TestAgentInstructions_ConciseBootAndDeferredScope(t *testing.T) {
	for _, hub := range []bool{false, true} {
		data := minimalTemplateData()
		data.SyncHubConfigured = hub
		dir := t.TempDir()
		gen, err := NewEmbeddedGenerator(dir, data, nil)
		require.NoError(t, err)
		_, err = gen.Generate(true)
		require.NoError(t, err)
		claude := readAgentInstruction(t, filepath.Join(dir, "CLAUDE.md"))
		start := strings.Index(claude, "\n2. ")
		require.GreaterOrEqual(t, start, 0)
		end := strings.Index(claude[start+1:], "\n3. ")
		require.GreaterOrEqual(t, end, 0)
		rule := strings.TrimSpace(claude[start : start+1+end])
		assert.Equal(t, conciseRegistrationRule, rule, "Rule 2 remains one physical line with boot, repeat and session instructions")
		assert.LessOrEqual(t, len(strings.Fields(rule)), 40)
		require.Contains(t, claude, "3. Claim nodes before working on them")
		for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
			body := readAgentInstruction(t, filepath.Join(dir, name))
			assert.NotContains(t, body, "## Inspecting deferred tasks")
			assert.NotContains(t, body, "⏸ deferred (until", "inspection detail belongs in references and skills")
		}
		installed := t.TempDir()
		_, err = NewPluginInstaller(installed, data, nil).Install("claude-code", false)
		require.NoError(t, err)
		require.Contains(t, readAgentInstruction(t, filepath.Join(installed, "CLAUDE.md")), conciseRegistrationRule)
	}
}

func TestAgentInstructions_RetainDetailedReferences(t *testing.T) {
	for _, rel := range []string{
		"internal/docs/templates/skills/task_execution.md.tmpl",
		".claude-plugin/skills/mtix-task-execution.md",
		".codex-plugin/skills/task-execution/SKILL.md",
		"USERMANUAL.md",
	} {
		body := readAgentInstruction(t, filepath.Join("..", "..", rel))
		require.Contains(t, body, "⏸ deferred (until", rel)
		require.Contains(t, body, "stored record unchanged", rel)
	}
	manual := readAgentInstruction(t, filepath.Join("..", "..", "USERMANUAL.md"))
	require.Contains(t, manual, "already_registered")
	require.Contains(t, manual, "preserves the existing state, work assignment, project, and active session")
	cli := readAgentInstruction(t, filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.Contains(t, cli, "The stored wake time is the defer_until field of mtix show <id> --json.")
}

// The obsolete MCP name was already absent; this is an absence regression guard.
func TestAgentInstructions_NoNonexistentRegistrationMCP(t *testing.T) {
	for _, root := range []string{"internal/docs/templates", ".claude-plugin", ".codex-plugin", "docs"} {
		err := filepath.WalkDir(filepath.Join("..", "..", root), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && (strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".tmpl")) {
				require.NotContains(t, readAgentInstruction(t, path), "mcp__mtix__mtix_agent_register", path)
			}
			return nil
		})
		require.NoError(t, err)
	}
}

func readAgentInstruction(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // repository or t.TempDir() paths supplied by these tests
	require.NoError(t, err)
	return string(body)
}
