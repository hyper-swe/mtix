// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Conflict history guidance must explain bounds and avoid claiming missing history is proof.
package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowHistoryGuidance_ExplainsEvidenceAndKeepsMirrors(t *testing.T) {
	skill := renderedSyncSkill(t)
	for _, phrase := range []string{"five entries per side", "shared entry", "no proof of no changes", "resolution", "history.export"} {
		require.Contains(t, skill, phrase)
	}
	for _, path := range []string{".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"} {
		mirror, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		require.Equal(t, skillBody(t, skill), skillBody(t, string(mirror)))
	}
	manual, err := os.ReadFile("../../USERMANUAL.md")
	require.NoError(t, err)
	require.Contains(t, string(manual), "five entries per side")
	require.Contains(t, string(manual), "no proof of no changes")
}
