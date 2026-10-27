// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeBudgetGuidance_SettingRowsDescribeCurrentEnforcement(t *testing.T) {
	for _, path := range []string{"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md", "USERMANUAL.md"} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, key := range []string{"wake.wakes_per_hour", "wake.backoff_after_unacked"} {
				require.Contains(t, wakeBudgetSettingLine(t, body, key), "callable dispatcher")
			}
			for _, key := range []string{"wake.per_sender_per_hour", "wake.remote_awake_daily_cap", "wake.reply_extensions_per_hour"} {
				require.Contains(t, wakeBudgetSettingLine(t, body, key), "stored, not yet enforced")
			}
			require.Contains(t, body, "budget.rewind")
			require.NotContains(t, body, "Scanner discovery, budget and\nback-off enforcement")
		})
	}
}

func wakeBudgetSettingLine(t *testing.T, body, key string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "| `"+key+"` |") {
			return line
		}
	}
	t.Fatalf("settings table must describe %s", key)
	return ""
}

func TestWakeBudgetGuidance_AckRoutineAndScannerBoundary(t *testing.T) {
	for _, path := range []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, phrase := range []string{"callable dispatcher", "across boards", "new acknowledgement", "already acknowledged", "budget.rewind", "ack after handling"} {
				require.Contains(t, body, phrase)
			}
			require.Contains(t, body, "when the wake scanner ships")
			require.NotContains(t, body, "Scanner discovery, budget/back-off enforcement")
		})
	}
}
