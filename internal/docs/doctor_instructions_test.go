// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor guidance tests preserve the same current and future contracts on shipped surfaces.
package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doctorGuidanceSection(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	_, rest, found := strings.Cut(string(data), "## Health checks\n")
	require.True(t, found, path)
	section, _, _ := strings.Cut(rest, "\n## ")
	return strings.TrimSpace(section)
}

func TestDoctorGuidance_ManualTemplatesAndMirrorsMatch(t *testing.T) {
	root := filepath.Join("..", "..")
	want := doctorGuidanceSection(t, filepath.Join(root, "USERMANUAL.md"))
	for _, path := range []string{
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md",
		"internal/docs/templates/troubleshooting.md.tmpl", "docs/TROUBLESHOOTING.md",
	} {
		assert.Equal(t, want, doctorGuidanceSection(t, filepath.Join(root, path)), path)
	}
	for _, phrase := range []string{"`mtix doctor --json`", "`mtix doctor --fix`", "Exit 2", "non-grant", "four", "FUTURE", "MTIX-112.19.2", "`docs.stale`", "mtix -C <board-root> docs generate", "including with `--fix`"} {
		if phrase == "four" {
			assert.Contains(t, want, "exactly `id`, `status`, `detail`")
			continue
		}
		assert.Contains(t, want, phrase)
	}
}
