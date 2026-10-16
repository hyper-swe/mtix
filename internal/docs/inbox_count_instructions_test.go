// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Count instructions ship with managed guidance and matching plugin mirrors.
package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxCountInstructions_MirrorsAndManagedDocs_ExplainReadOnlyPolling(t *testing.T) {
	paths := []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md", "USERMANUAL.md"}
	var want string
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		_, section, ok := strings.Cut(string(raw), "## Read-only inbox counts\n")
		require.True(t, ok, path)
		section, _, _ = strings.Cut(section, "\n## ")
		if want == "" {
			want = section
		} else {
			require.Equal(t, want, section, path)
		}
		for _, phrase := range []string{"--count --all", "pending", "total", "last_seen", "--wait=false", "--format=", "retained local journal", "no hub contact"} {
			require.Contains(t, section, phrase, path)
		}
	}
	gen, err := NewEmbeddedGenerator(t.TempDir(), minimalTemplateData(), nil)
	require.NoError(t, err)
	for _, template := range []string{"claude.md.tmpl", "agents.md.tmpl", "session_protocol.md.tmpl"} {
		// Source and generated template parsing remain checked by the generator suite.
		raw, err := os.ReadFile(filepath.Join("templates", template))
		require.NoError(t, err)
		require.Contains(t, string(raw), "--count --all")
	}
	_, err = gen.Generate(true)
	require.NoError(t, err)
}
