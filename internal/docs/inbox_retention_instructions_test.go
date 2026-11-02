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

// MTIX-112.2: the retention guarantee ships in the user manual, the
// multi-agent skill and both plugin mirrors with identical text, and in the
// short managed sections of the generated agent instructions.
func TestInboxRetentionInstructions_FullSectionMirrored_ShortSectionManaged(t *testing.T) {
	full := []string{"USERMANUAL.md", "internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"}
	var want string
	for _, path := range full {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		_, section, ok := strings.Cut(string(raw), "## Inbox retention guarantee\n")
		require.True(t, ok, path)
		section, _, _ = strings.Cut(section, "\n## ")
		if want == "" {
			want = section
		} else {
			require.Equal(t, want, section, path)
		}
		for _, phrase := range []string{"no TTL", "until that agent acknowledges it", "Acknowledgements are not synced", "--history",
			"removes the per-event acknowledgement rows", "`mtix import --mode replace`", "--discard-local", "tasks.json", "inbox_deliveries", "agent_inbox_ack", "agent_inbox_cursor", "no hub contact", "Apart from the exceptions below", "refuses a replace that would drop a task its sync log holds", "removed in the same step"} {
			require.Contains(t, section, phrase, path)
		}
	}
	for _, path := range []string{"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl", "internal/docs/templates/session_protocol.md.tmpl", "docs/AGENTS.md"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		_, section, ok := strings.Cut(string(raw), "## Inbox retention\n")
		require.True(t, ok, path)
		section, _, _ = strings.Cut(section, "\n## ")
		for _, phrase := range []string{"no TTL", "acks are not synced", "--history", "discard-local"} {
			require.Contains(t, section, phrase, path)
		}
	}
}
