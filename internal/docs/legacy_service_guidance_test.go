// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package docs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A 0.5.x per-project service keeps pull and hook duty until migrated (ADR-007 D11, Commandment 15).
func TestLegacyServiceGuidance_CoexistenceDoctorAndMigration(t *testing.T) {
	full := []string{"USERMANUAL.md", "internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"}
	for _, path := range full {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, phrase := range []string{"dispatcher.legacy-service", "daemon_live", "unit_installed", "keeps pull and hook duty", "only the wake scanner", "never writes the daemon's PID file", "belongs to another program", "`mtix daemon status`", "counts as live, the safe side", "mtix daemon uninstall", "Do not stop, uninstall or migrate"} {
				require.Contains(t, body, phrase)
			}
			require.NotContains(t, body, "mtix setup", "the setup command ships in a later build")
		})
	}
	for _, path := range []string{"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl"} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, phrase := range []string{"dispatcher.legacy-service", "keeps pull and hook duty", "mtix daemon uninstall", "do not stop, uninstall or migrate"} {
				require.Contains(t, body, phrase)
			}
			require.NotContains(t, body, "mtix setup")
		})
	}
}
