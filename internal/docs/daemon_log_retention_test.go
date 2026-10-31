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
			for _, phrase := range daemonRetentionSettings() {
				require.Contains(t, body, phrase)
			}
			for _, phrase := range []string{"daemon.logs", "10 MiB", "--fix", "newsyslog", "10240", "SIGTERM", "stale PID", "SystemMaxUse=100M", "RuntimeMaxUse=100M", "copytruncate", "Debug-only"} {
				require.Contains(t, body, phrase)
			}
		})
	}
}

func daemonRetentionSettings() []string {
	return []string{
		"10485760 bytes",
		"/absolute/project/.mtix/logs/daemon.out.log USER:GROUP 600 3 10240 * - /absolute/project/.mtix/sync.daemon.pid 15",
		"/absolute/project/.mtix/logs/daemon.err.log USER:GROUP 600 3 10240 * - /absolute/project/.mtix/sync.daemon.pid 15",
		`"/absolute/project/.mtix/logs/daemon.out.log" "/absolute/project/.mtix/logs/daemon.err.log" {
    su USER GROUP
    size 10M
    rotate 3
    copytruncate
    missingok
    notifempty
}`,
		`[Journal]
SystemMaxUse=100M
RuntimeMaxUse=100M
SystemMaxFileSize=10M
RuntimeMaxFileSize=10M`,
		"one SIGTERM per invocation",
		"newsyslog.conf(5)",
		"symlink, not inspected",
	}
}
