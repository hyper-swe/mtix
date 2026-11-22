// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Detached listener guidance covers verification and preserves waiter ownership.
package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDetachedListenerGuidance_ManagedSectionsPointToSkill pins the short form
// of the always-loaded managed sections: the row, the fix and the sync skill.
func TestDetachedListenerGuidance_ManagedSectionsPointToSkill(t *testing.T) {
	for _, path := range []string{"templates/agents.md.tmpl", "templates/claude.md.tmpl"} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, want := range []string{"daemon.detached-listener",
				"mtix daemon stop --listener && mtix daemon status --listener", "The sync skill has the full runbook"} {
				require.Contains(t, string(body), want, path)
			}
			require.NotContains(t, string(body), "waiter leases.\n\n\n", "no extra blank lines after the paragraph")
		})
	}
}

func TestDetachedListenerGuidance_AllCopies(t *testing.T) {
	for _, path := range []string{
		"templates/skills/sync.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
		"templates/troubleshooting.md.tmpl",
		"../../docs/TROUBLESHOOTING.md", "../../USERMANUAL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, want := range []string{"daemon.detached-listener", "mtix daemon stop --listener",
				"mtix daemon status --listener", "exit 10", "does not release", "waiter leases"} {
				require.True(t, strings.Contains(string(body), want), "%s must contain %q", path, want)
			}
		})
	}
}
