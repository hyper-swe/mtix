// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLearnedLaneGuidance_ShippedRelease_RequiresExplicitRegistration(t *testing.T) {
	for _, path := range []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md", "USERMANUAL.md"} {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		require.NoError(t, err)
		for _, term := range []string{"pass their id explicitly", "Callable identity resolution", "No shipped command reads"} {
			require.Contains(t, string(raw), term, path)
		}
	}
}
