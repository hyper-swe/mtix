// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-21 §6.2 (MTIX-301): every shipped relay runbook says a publisher killed
// between append and cursor advance resumes with no operator action.
func TestRelayCrashResumeGuidance_AllSurfaces(t *testing.T) {
	paths := []string{
		"templates/agents.md.tmpl",
		"templates/claude.md.tmpl",
		"templates/troubleshooting.md.tmpl",
		"templates/skills/sync.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
		"../../docs/AGENTS.md",
		"../../docs/codex/AGENTS.md",
		"../../docs/TROUBLESHOOTING.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(body), "resumes on restart with no operator action: it checks the records past its cursor against its journal")
		})
	}
}
