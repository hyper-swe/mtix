// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-19.1: the recovery limits and conservative cost must reach every published mirror.
func TestInboxRecoveryLimits_AllMirrorsAgree(t *testing.T) {
	paths := []string{"USERMANUAL.md", "docs/EXPORT-FORMAT.md", "internal/docs/templates/skills/admin.md.tmpl", "internal/docs/templates/skills/multi_agent.md.tmpl", "internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".claude-plugin/skills/mtix-multi-agent.md", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/admin/SKILL.md", ".codex-plugin/skills/multi-agent/SKILL.md", ".codex-plugin/skills/sync/SKILL.md"}
	var want string
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		require.NotContains(t, string(data), "keep the damaged database when recovery is incomplete", path)
		require.NotContains(t, string(data), "Keep the damaged database and any private export.", path)
		_, text, ok := strings.Cut(string(data), "Inbox salvage checks each rowid seek")
		require.True(t, ok, path)
		text, _, ok = strings.Cut(text, "if it cannot protect the file, it writes no recovered export.")
		require.True(t, ok, path)
		if want == "" {
			want = text
		} else {
			require.Equal(t, want, text, path)
		}
		for _, phrase := range []string{"WAL header", "absent, emptied, or shortened exactly on a frame boundary", "committed records may be missing even under a complete report", "stale tail", "committed records (including tickets and mail) may be missing", "lost count is unknown", "already-read mail", "does not prove the store is damaged", "could not be proven", "`mtix export`", "0.5.4 or earlier", "`idx_sync_events_comment_addressee`", "`$.to`", "`sync_events.payload`", "`agent_inbox_cursor.cursor`"} {
			require.Contains(t, text, phrase, path)
		}
	}
}
