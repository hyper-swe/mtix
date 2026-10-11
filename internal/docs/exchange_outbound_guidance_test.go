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

func TestPluginInstaller_OutboundProvenance_ExplainsEligibleNewPull(t *testing.T) {
	skill := renderedSyncSkill(t)
	for _, phrase := range []string{"including held push events", "same board snapshot", "pending-outbound", "new, complete", "push alone does not publish", "absent or null", "outbound-boundary-unknown", "without rewriting", "not a recorded failed exchange", "saved omission takes precedence"} {
		require.Contains(t, skill, phrase)
	}
	for _, path := range []string{".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"} {
		mirror, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		require.Equal(t, skillBody(t, skill), skillBody(t, string(mirror)))
	}
}

func TestDocsGenerate_OutboundGuidance_RespectsHubConfiguration(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "no hub", true: "configured hub"}[configured], func(t *testing.T) {
			data := minimalTemplateData()
			data.SyncHubConfigured = configured
			sections := generatedSyncSections(t, data)
			require.Equal(t, sections["CLAUDE.md"], sections["AGENTS.md"])
			for _, section := range sections {
				if configured {
					require.LessOrEqual(t, contentLines(section), 15)
					require.Contains(t, section, "pending or unknown outbound state requires push then a new complete pull")
					require.Less(t, strings.Index(section, "`mtix sync push`"), strings.Index(section, "`mtix sync pull`"))
				} else {
					require.Equal(t, 1, contentLines(section))
					require.Contains(t, section, "`mtix-sync` skill")
					require.NotContains(t, section, "outbound")
					require.NotContains(t, section, "mtix sync push")
				}
			}
		})
	}
}
