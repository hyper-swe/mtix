// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Freshness guidance teaches agents to retry unverified CLI reads safely.
package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutoImportFreshness_Guidance_ExplainsBusyReadRecovery(t *testing.T) {
	for _, path := range []string{
		"templates/agents.md.tmpl", "templates/claude.md.tmpl", "templates/skill.md.tmpl",
		"templates/skills/sync.md.tmpl", "../../docs/SKILL.md",
		"../../.claude-plugin/skills/mtix-sync.md", "../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, phrase := range []string{
				"stale", "stale_reason", "sync_lock_busy", "retry",
				"read responses only", "never repeat a successful write",
			} {
				require.Contains(t, string(body), phrase)
			}
		})
	}
}
