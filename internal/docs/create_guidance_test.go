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
