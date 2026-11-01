// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent-facing wake delivery status guidance (MTIX-112.40.6, Commandment 15).
package docs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeDeliveryStatusGuidance_Skills_DocumentStatesCommandsAndDoctorRows(t *testing.T) {
	paths := []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md", "USERMANUAL.md"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, state := range model.WakeStates() {
				require.Contains(t, body, "`"+string(state)+"`", "state "+state)
			}
			for _, phrase := range []string{
				"mtix inbox status --agent", "mtix comment --to", "wake.confirm_window", "never written as an inbox event",
				"wake.listener.routes", "wake.listener.dispatcher", "wake.listener.unconfirmed-rate",
				"wake.listener.hook-installed", "wake.listener.waiter-armed", "wake.listener.route-contract", "wake.listener.bypass-hold",
				"data.reachable", "data.blocked_by", "seeded-only", "MTIX-112.40.10", "never silently skipped",
				"ordinary setting and needs no terminal", "appear only on rows read from the wake ledger",
			} {
				require.Contains(t, body, phrase)
			}
		})
	}
}

func TestWakeDeliveryStatusGuidance_ManagedBriefings_PointAtStatusAndWindow(t *testing.T) {
	for _, name := range []string{"agents.md.tmpl", "claude.md.tmpl"} {
		body := readAgentInstruction(t, filepath.Join("templates", name))
		for _, phrase := range []string{"mtix inbox status --agent", "wake.confirm_window", "wake.listener.*", "seeded-only", "MTIX-112.40.10", "applies `wake.confirm_window` now"} {
			require.Contains(t, body, phrase, name)
		}
	}
}

func TestWakeDeliveryStatusGuidance_Admin_ListsConfirmWindowSetting(t *testing.T) {
	for _, path := range []string{"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md", "USERMANUAL.md"} {
		body := readAgentInstruction(t, filepath.Join("..", "..", path))
		require.Contains(t, body, "| `wake.confirm_window` | `30` |", path)
		require.Contains(t, body, "An ordinary setting, not a guard", path)
		require.Contains(t, body, "never needs a terminal", path)
	}
}
