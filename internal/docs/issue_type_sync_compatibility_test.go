// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Shipped sync guidance must explain the permanent mixed-version classification gap.
import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueTypeSyncGuidance_RequiresEveryReplicaUpgrade(t *testing.T) {
	paths := []string{"../../docs/SYNC-DESIGN.md", "../../USERMANUAL.md", "templates/skill.md.tmpl", "../../docs/SKILL.md", "templates/skills/planning.md.tmpl", "../../.claude-plugin/skills/mtix-planning.md", "../../.codex-plugin/skills/planning/SKILL.md"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Contains(t, string(content), "Upgrade every replica before relying on synchronized issue type.")
			assert.Contains(t, string(content), "A node created while a 0.5.4 replica is attached stays unclassified on that replica after upgrade; already-applied create events are not replayed.")
		})
	}
}

func TestIssueTypeUpdateSyncGuidance_QuarantineAndUpgradeRetry(t *testing.T) {
	paths := []string{
		"../../docs/SYNC-DESIGN.md", "../../USERMANUAL.md", "templates/skill.md.tmpl",
		"../../docs/SKILL.md", "templates/skills/planning.md.tmpl",
		"../../.claude-plugin/skills/mtix-planning.md", "../../.codex-plugin/skills/planning/SKILL.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			text := string(content)
			for _, statement := range []string{
				"A 0.5.4 replica quarantines `issue_type` updates and continues pulling other events.",
				"These updates remain quarantined until the replica is upgraded.",
				"After upgrading, run `mtix sync pull` to retry them automatically",
				"retry runs locally before contacting the hub",
				"Updates that apply successfully leave the quarantine.",
				"Use `mtix sync quarantine list` to inspect any events still held.",
			} {
				assert.Contains(t, text, statement)
			}
			assert.NotContains(t, text, "sync quarantine retry", "recovery uses the existing pull command")
		})
	}
}
