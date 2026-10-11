// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPendingExports_RenderedRecoveryGuidance(t *testing.T) {
	data := minimalTemplateData()
	dir := t.TempDir()
	installer := NewPluginInstaller(dir, data, nil)
	_, err := installer.Install("claude-code", false)
	require.NoError(t, err)
	_, err = installer.Install("codex", false)
	require.NoError(t, err)
	for _, name := range []string{"mtix-sync.md", "mtix-admin.md"} {
		body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", name))
		require.NoError(t, err)
		for _, phrase := range []string{"pending_exports.count", "oldest_at", "oldest_age_seconds", "mtix list", "one hour", "refusal"} {
			require.Contains(t, string(body), phrase, name)
		}
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		require.Contains(t, string(body), "Mirror exports retain one durable local generation")
		require.Contains(t, string(body), "doctor's `mtix list` command")
		require.Contains(t, string(body), "sync skill for pulled-board refusals and migration limits")
	}
}
