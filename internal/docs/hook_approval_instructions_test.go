// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHookApproval_Instructions_DescribeOperatorWorkflow(t *testing.T) {
	root := filepath.Join("..", "..")
	paths := []string{
		"internal/docs/templates/skills/admin.md.tmpl",
		"internal/docs/templates/skills/multi_agent.md.tmpl",
		"internal/docs/templates/agents.md.tmpl",
		"internal/docs/templates/claude.md.tmpl",
		".codex-plugin/skills/admin/SKILL.md",
		".codex-plugin/skills/multi-agent/SKILL.md",
		".claude-plugin/skills/mtix-admin.md",
		".claude-plugin/skills/mtix-multi-agent.md",
		"USERMANUAL.md", "docs/AGENTS.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, path))
			require.NoError(t, err)
			text := string(raw)
			require.Contains(t, text, "mtix hooks trust <hook>")
			require.Contains(t, text, "mtix hooks status")
			require.Contains(t, text, "operator terminal")
		})
	}
}

func TestHookApproval_Instructions_RefreshManagedSections(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			custom := "# Local guide\n\noperator custom text\n\n<!-- AUTO-GENERATED: MCP_TOOLS -->\nold tools\n<!-- END AUTO-GENERATED -->\n\nlocal footer\n"
			require.NoError(t, os.WriteFile(path, []byte(custom), 0600))
			generator, err := NewEmbeddedGenerator(dir, minimalTemplateData(), nil)
			require.NoError(t, err)
			_, err = generator.Generate(true)
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(raw), "operator custom text")
			require.Contains(t, string(raw), "local footer")
			sections := extractSections(string(raw))
			require.Contains(t, sections["HOOK_APPROVALS"], "mtix hooks trust <hook>")
			require.Contains(t, sections["HOOK_APPROVALS"], "operator terminal")
			if name == "AGENTS.md" {
				tracked, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
				require.NoError(t, err)
				require.Equal(t, sections["HOOK_APPROVALS"], extractSections(string(tracked))["HOOK_APPROVALS"])
			}
		})
	}
}
