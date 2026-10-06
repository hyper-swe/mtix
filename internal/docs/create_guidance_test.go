// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Creation guidance must teach classification separately from hierarchy and atomic assignment.
package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateGuidance_ExplainsClassificationAndAssignment(t *testing.T) {
	for _, path := range []string{"templates/skill.md.tmpl", "templates/skills/planning.md.tmpl", "../../USERMANUAL.md"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			text := strings.ToLower(string(raw))
			for _, word := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "unset", "depth", "atomic", "creator"} {
				assert.Contains(t, text, word)
			}
		})
	}
}

func TestCreateGuidance_ExplainsSharedPrefixValidation(t *testing.T) {
	paths := []string{"templates/agents.md.tmpl", "templates/claude.md.tmpl", "templates/skill.md.tmpl", "../../docs/SKILL.md", "templates/skills/planning.md.tmpl", "../../.claude-plugin/skills/mtix-planning.md", "../../.codex-plugin/skills/planning/SKILL.md", "../../USERMANUAL.md"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, fragment := range []string{"^[A-Z][A-Z0-9-]{0,19}$", "INVALID_INPUT", "CLI", "REST", "gRPC", "MCP", "underscore"} {
				assert.Contains(t, string(raw), fragment)
			}
		})
	}
}
