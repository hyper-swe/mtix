// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Planning mirrors must teach agents to discover accepted dependency types.
import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanningSkills_DependencyHelpHintParity(t *testing.T) {
	for _, path := range []string{"templates/skills/planning.md.tmpl", "../../.claude-plugin/skills/mtix-planning.md", "../../.codex-plugin/skills/planning/SKILL.md"} {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Contains(t, string(content), "`mtix dep add --help` lists every accepted type. Use the same type with `mtix dep remove`.")
		})
	}
}
