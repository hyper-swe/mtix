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
		"decided by the\nphysical directory that holds it",
		"one only when that directory has no link left in it",
		"prints the program it will approve before it writes the approval",
	}
	for _, rel := range files {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
			require.NoError(t, err)
			require.True(t, strings.Contains(strings.Join(strings.Fields(string(body)), " "),
				"check the link and directory for the named env wrapper or interpreter"))
			require.True(t, strings.Contains(strings.Join(strings.Fields(string(body)), " "),
				"a link that cannot be read is also not known and is refused"))
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

// The link-limit hardening refuses a link that cannot be read as well as an
// over-long chain; the release notes must say both.
func TestChangelog_LinkLimitEntry_MentionsUnreadableLink(t *testing.T) {
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	require.NoError(t, err)
	var entry string
	for _, line := range strings.Split(string(changelog), "\n") {
		if strings.Contains(line, "(MTIX-112.34.2.3.3)") {
			entry = line
			break
		}
	}
	require.NotEmpty(t, entry, "link-limit changelog entry not found")
	require.Contains(t, entry, "over-long link chains")
	require.Contains(t, entry, "a link that cannot be read is refused as well")
}

func TestChangelog_StrictPinRefusal_NamesLinkRemedy(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	require.NoError(t, err)
	_, rest, found := strings.Cut(string(body), "## [Unreleased]\n")
	require.True(t, found)
	unreleased, _, _ := strings.Cut(rest, "\n## ")
	for _, line := range strings.Split(unreleased, "\n") {
		if strings.Contains(line, "(MTIX-112.34.2.3.1)") {
			require.Contains(t, line, "check the link and directory for the named env wrapper or interpreter")
			return
		}
	}
	t.Fatal("missing strict pin refusal entry under Unreleased")
}
