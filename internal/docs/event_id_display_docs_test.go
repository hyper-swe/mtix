// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Human-facing event ids are shown double-quoted and ASCII-escaped
// (MTIX-187.4.2). The instructions an agent reads must say so.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventIDDisplay_InstructionsExplainQuotedIDs(t *testing.T) {
	want := map[string]string{
		"internal/docs/templates/skills/sync.md.tmpl": "double-quoted with control and non-ASCII characters escaped",
		".claude-plugin/skills/mtix-sync.md":          "double-quoted with control and non-ASCII characters escaped",
		".codex-plugin/skills/sync/SKILL.md":          "double-quoted with control and non-ASCII characters escaped",
		"USERMANUAL.md":                               "every event id is shown double-quoted",
	}
	for path, phrase := range want {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			require.Contains(t, string(body), phrase)
			require.Contains(t, string(body), "--json")
			require.NotContains(t, string(body), "without the quotes")
			require.Contains(t, string(body), "display only")
		})
	}
}
