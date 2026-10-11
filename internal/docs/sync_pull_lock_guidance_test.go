// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Installed sync guidance distinguishes skipped hook pulls from completed work.
package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncPullLockGuidance_SkillAndMirrors(t *testing.T) {
	skill := renderedSyncSkill(t)
	for _, want := range []string{"pull was not performed", "Retry after the current owner finishes", "Filesystem acquisition failures and cancellation remain errors", "Never delete the sync lock file"} {
		require.Contains(t, skill, want)
	}
	claude, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "skills", "mtix-sync.md"))
	require.NoError(t, err)
	require.Equal(t, skill, string(claude))
	codex, err := os.ReadFile(filepath.Join("..", "..", ".codex-plugin", "skills", "sync", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, skillBody(t, skill), skillBody(t, string(codex)))
}

func TestSyncPullLockGuidance_ManagedConfiguredAndUnconfigured(t *testing.T) {
	for _, configured := range []bool{true, false} {
		t.Run(map[bool]string{true: "configured", false: "unconfigured"}[configured], func(t *testing.T) {
			data := minimalTemplateData()
			data.SyncHubConfigured = configured
			dir := t.TempDir()
			gen, err := NewEmbeddedGenerator(dir, data, nil)
			require.NoError(t, err)
			_, err = gen.Generate(false)
			require.NoError(t, err)
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				raw, err := os.ReadFile(filepath.Join(dir, name))
				require.NoError(t, err)
				if configured {
					require.Contains(t, string(raw), "sync lock warns that pull was not performed; retry after the current owner finishes")
					require.Contains(t, string(raw), "Push first")
				} else {
					require.Contains(t, string(raw), "No sync hub is configured")
					require.NotContains(t, string(raw), "sync lock warns")
				}
			}
			raw, err := os.ReadFile(filepath.Join(dir, "TROUBLESHOOTING.md"))
			require.NoError(t, err)
			require.Contains(t, string(raw), "Pull sync lock is busy")
			require.Contains(t, string(raw), "no pull was completed or automatically retried")
		})
	}
}
