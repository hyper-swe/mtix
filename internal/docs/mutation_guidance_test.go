// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Freshness guidance distinguishes cached reads from completed writes.
package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutoImportFreshness_Guidance_SeparatesReadsFromSuccessfulWrites(t *testing.T) {
	for _, path := range []string{
		"templates/agents.md.tmpl", "templates/claude.md.tmpl", "templates/skill.md.tmpl",
		"templates/skills/sync.md.tmpl", "../../docs/SKILL.md",
		"../../.claude-plugin/skills/mtix-sync.md", "../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			guidance := freshnessGuidanceSection(t, body)
			assert.Contains(t, guidance, "read responses only", "freshness must be documented as read-only")
			assert.Contains(t, guidance, "never repeat a successful write", "busy ownership must not invite duplicate mutations")
		})
	}
}

func freshnessGuidanceSection(t *testing.T, body []byte) string {
	t.Helper()
	_, tail, found := strings.Cut(string(body), "CLI read freshness\n")
	require.True(t, found, "freshness guidance section must exist")
	section, _, _ := strings.Cut(tail, "\n#")
	return strings.ToLower(section)
}
