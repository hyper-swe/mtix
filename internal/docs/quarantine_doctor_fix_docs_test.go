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

func TestQuarantineDoctorFix_InstructionsNameHandleCommand(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
		"internal/docs/templates/agents.md.tmpl", "internal/docs/templates/claude.md.tmpl", "docs/AGENTS.md", "USERMANUAL.md", "docs/SYNC-PROTOCOL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			require.False(t, strings.Contains(string(body), "reviewed invalid node id; accepted as unapplied"), "canned acknowledgement reason in %s", path)
			require.Contains(t, string(body), "Doctor's Fix names the event by its handle")
			require.Contains(t, string(body), "`mtix sync quarantine acknowledge --handle <handle> --reason '<your explanation>'`")
			require.Contains(t, string(body), "without `--reason` the command is refused")
			require.Contains(t, string(body), "Replace `<your explanation>`")
			require.Contains(t, string(body), "Then run doctor again after each acknowledgement")
			if strings.HasSuffix(path, "agents.md.tmpl") || strings.HasSuffix(path, "claude.md.tmpl") || path == "docs/AGENTS.md" {
				// The always-loaded guide must not name internal field names (ADR-003 section 14).
				require.Contains(t, string(body), "`handle` and the full digest in `--json`")
			} else {
				require.Contains(t, string(body), "`handle` and `event_id_sha256` in `--json`")
			}
		})
	}
}
