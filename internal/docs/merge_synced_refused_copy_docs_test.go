// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.17.16.16: every doc copy that tells an agent how to end a
// merge_synced refusal names the copy mtix sync --fix keeps of the refused
// file, and says that an added-only import stays local only.
func TestMergeSyncedDocs_NameRefusedCopyAndLocalOnlyImport(t *testing.T) {
	copyPath := "`.mtix/data/refused-boards/tasks-<UTC time>.json`"
	paths := []string{
		"templates/skills/sync.md.tmpl",
		"templates/troubleshooting.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
		"../../CHANGELOG.md",
		"../../docs/TROUBLESHOOTING.md",
		"../../docs/SYNC-PROTOCOL.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(body), copyPath)
			require.Contains(t, string(body), "local only")
			require.Contains(t, string(body), "differs from the last export")
		})
	}
}

// TestPendingImportRefusalDocs_NameCheckNoticeAndChoice: every doc copy names
// the pending import refusal doctor check, the local-only notice, and that the
// user chooses when the fix lists several ways out.
func TestPendingImportRefusalDocs_NameCheckNoticeAndChoice(t *testing.T) {
	paths := []string{
		"templates/skills/sync.md.tmpl",
		"templates/troubleshooting.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
		"../../CHANGELOG.md",
		"../../docs/TROUBLESHOOTING.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			text := string(body)
			if strings.Contains(path, "sync") && !strings.Contains(path, "TROUBLE") {
				require.Contains(t, text, "never move or delete it")
			}
			require.Contains(t, text, "pending import refusal")
			require.Contains(t, text, "wrote no sync event and stay local only unless their author pushed them to the hub")
			require.Contains(t, text, "user choose")
			require.NotContains(t, text, "holds the exact command")
		})
	}
}
