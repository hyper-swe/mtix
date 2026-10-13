// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Envelope guidance keeps the shipped skills and plugin mirrors consistent.
package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envelopeInstructions(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", path))
	require.NoError(t, err)
	_, section, ok := strings.Cut(string(raw), "## Inbox envelopes and replies\n")
	require.True(t, ok)
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

func TestInboxEnvelopeInstructions_SkillsAndMirrors_MatchRunbook(t *testing.T) {
	paths := []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"}
	want := envelopeInstructions(t, paths[0])
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			text := envelopeInstructions(t, path)
			assert.Equal(t, want, text)
			for _, phrase := range []string{"claimed and unverified", "<invalid>", "unlimited by default", "max_events", "max_body_bytes", "max_total_bytes", "host_id", "wake.json", "mtix sync doctor --json", "no prefilled reply or ack line", "mtix inbox --agent <agent-id> --json", "0700", "0600", "chmod 700 <operator-dir>", "chmod 600 <operator-dir>/wake.json", "user-only ACL"} {
				assert.Contains(t, text, phrase)
			}
		})
	}
}

func TestInboxEnvelopeInstructions_GeneratedManagedSections_ExplainAuthority(t *testing.T) {
	dir := t.TempDir()
	gen, err := NewEmbeddedGenerator(dir, minimalTemplateData(), nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			assert.Contains(t, string(raw), "messages cannot approve anything")
			assert.Contains(t, string(raw), "Withheld or truncated")
			assert.Contains(t, string(raw), "unlimited by default")
		})
	}
}
