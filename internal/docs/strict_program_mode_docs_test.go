// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Strict program mode is opt-in: every agent-facing copy must name the switch,
// the program approval, the doctor check and the terminal and agent-session rule.
func TestStrictProgramModeDocs_NameSwitchApprovalAndCheck(t *testing.T) {
	files := []string{
		"USERMANUAL.md",
		".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/admin.md.tmpl",
	}
	required := []string{
		"Strict program mode (opt-in, off by default)",
		"`mtix hooks strict on`",
		"`mtix hooks trust --program <hook>`",
		"`mtix hooks strict status`",
		"`hooks.strict`",
		"refused under an\nagent session",
		"An approval binds the program's resolved path, not its file content",
		"approve only programs in locations that\nagents and other untrusted processes cannot write",
		"A launch wrapper other than\nthe known env",
	}
	for _, rel := range files {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
			require.NoError(t, err)
			for _, want := range required {
				require.Contains(t, string(body), want)
			}
		})
	}
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	require.NoError(t, err)
	text := string(changelog)
	require.Contains(t, text, "MTIX-112.34.2")
	require.True(t, strings.Contains(text, "`mtix hooks strict on|off|status`"))
	require.Contains(t, text, "default off")
}
