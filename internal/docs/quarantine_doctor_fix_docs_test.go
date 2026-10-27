// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Concrete quarantine doctor instructions must travel to generated and mirrored docs.

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuarantineDoctorFix_InstructionsNameFirstEventAndCount(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
		"internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl", "docs/AGENTS.md", "USERMANUAL.md", "docs/SYNC-PROTOCOL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			require.False(t, strings.Contains(string(body), "reviewed invalid node id; accepted as unapplied"), "canned acknowledgement reason in %s", path)
			require.Contains(t, string(body), "first eligible event id and eligible count")
			require.Contains(t, string(body), "run doctor again after each acknowledgement")
			require.Contains(t, string(body), "Add `--reason '<your explanation>'` before `--` in the printed command")
			require.Contains(t, string(body), "the printed command is refused until you supply it")
			require.Contains(t, string(body), "replace `<your explanation>` in the `--reason` value")
		})
	}
}
