// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Recovery display guidance must reach generated and mirrored instructions.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorRecoveryDisplay_Instructions(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
		"internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl", "docs/AGENTS.md", "USERMANUAL.md", "docs/SYNC-PROTOCOL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			require.Contains(t, string(body), "always printable and typeable, even when the id holds NUL, invalid UTF-8 or shell metacharacters")
			require.Contains(t, string(body), "report the original id to the operator")
			if strings.HasSuffix(path, "agents.md.tmpl") || strings.HasSuffix(path, "claude.md.tmpl") || path == "docs/AGENTS.md" {
				require.Contains(t, string(body), "shows invalid UTF-8 bytes as U+FFFD; the digest and the handle always name the exact stored bytes")
			} else {
				require.Contains(t, string(body), "shows invalid UTF-8 bytes as U+FFFD; `event_id_sha256` and the handle always name the exact stored bytes")
			}
		})
	}
}
