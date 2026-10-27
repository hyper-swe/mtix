// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestWakeSettingsGuidance_AdminAndManual_ExplainDefaultsAndOffSwitch(t *testing.T) {
	for _, path := range []string{"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md", "USERMANUAL.md"} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for key := range model.WakeSettingDefaults() {
				require.Contains(t, body, key)
			}
			for _, phrase := range []string{"mtix config set wake.live off", "--agent worker", "wake.settings", "settings.ignored-workspace-key", "You should not need to tune these settings", "MTIX_WAKE_HOP", "already-running session", "always on"} {
				require.Contains(t, body, phrase)
			}
		})
	}
}

func TestWakeSettingsGuidance_ManagedBriefings_ExplainControls(t *testing.T) {
	for _, name := range []string{"agents.md.tmpl", "claude.md.tmpl"} {
		body := readAgentInstruction(t, filepath.Join("templates", name))
		require.Contains(t, body, "mtix config set wake.live off")
		require.Contains(t, body, "wake.settings")
	}
}
