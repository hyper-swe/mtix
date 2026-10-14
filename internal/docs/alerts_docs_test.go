// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests operator diagnostic discovery and recovery across shipped guidance.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorAlertsDocs_GuidanceAndMirrors(t *testing.T) {
	set := loadShippedDocs(t)
	for _, path := range []string{"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl", "internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md", "USERMANUAL.md"} {
		require.Contains(t, set.all[path], "alerts.recent", path)
		require.Contains(t, set.all[path], "mtix sync doctor --json", path)
		require.Contains(t, set.all[path], "never wake", path)
	}
	for _, path := range []string{"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md", "USERMANUAL.md"} {
		require.Contains(t, set.all[path], "one logical row", path)
		require.Contains(t, set.all[path], "latest 20", path)
		require.Contains(t, set.all[path], "other checks", path)
		require.Contains(t, set.all[path], "preserve", path)
	}
}

func TestOperatorAlertsDocs_AdminMirrorBodyMatches(t *testing.T) {
	set := loadShippedDocs(t)
	_, claude, ok := strings.Cut(set.all[".claude-plugin/skills/mtix-admin.md"], "# MTIX — Administration\n")
	require.True(t, ok)
	_, codex, ok := strings.Cut(set.all[".codex-plugin/skills/admin/SKILL.md"], "# MTIX — Administration\n")
	require.True(t, ok)
	require.Equal(t, claude, codex)
}
