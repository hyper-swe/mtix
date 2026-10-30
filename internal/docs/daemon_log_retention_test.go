// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-228: installed guidance must carry actionable retention settings and limits.
func TestDaemonLogRetention_Guidance(t *testing.T) {
	bodies := map[string]string{"rendered sync skill": renderedSyncSkill(t)}
	for _, name := range []string{"USERMANUAL.md", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", name))
		require.NoError(t, err)
		bodies[name] = string(raw)
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			for _, phrase := range []string{"daemon.logs", "10 MiB", "--fix", "newsyslog", "10240", "SIGTERM", "stale PID", "SystemMaxUse=100M", "RuntimeMaxUse=100M", "copytruncate", "Debug-only"} {
				require.Contains(t, body, phrase)
			}
		})
	}
}
