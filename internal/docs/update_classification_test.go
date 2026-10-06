// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent surfaces teach the create/update/list classification contract (MTIX-107.60).
package docs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassificationDocs_UpdateListAndUnset(t *testing.T) {
	for _, rel := range []string{
		"internal/docs/templates/skill.md.tmpl", "internal/docs/templates/skills/planning.md.tmpl",
		"docs/SKILL.md", ".claude-plugin/skills/mtix-planning.md", ".codex-plugin/skills/planning/SKILL.md", "USERMANUAL.md",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", rel))
			for _, phrase := range []string{"mtix update TEST-1 --type refactor", "mtix update TEST-1 --type=", "mtix list --type story --issue-type bug", "omission preserves", "empty string clears", "Existing unclassified nodes remain unset", "update_field"} {
				assert.Contains(t, body, phrase)
			}
		})
	}
	for _, rel := range []string{"internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl", "docs/AGENTS.md", "docs/codex/AGENTS.md"} {
		body := readAgentInstruction(t, filepath.Join("..", "..", rel))
		assert.Contains(t, body, "update --type")
		assert.Contains(t, body, "list --issue-type")
		assert.Contains(t, body, "list --type")
	}
}
