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

// The hub mark is stored by sync doctor and by sync init, clone, pull and push
// after a successful hub connection. Relay attach, sync status and a failed
// connect do not store it, so the docs must not claim that any sync command does.
func TestHubMarkDocs_StoredBy_NamesTheCommandsThatStoreIt(t *testing.T) {
	files := []string{
		"USERMANUAL.md",
		"CHANGELOG.md",
		".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md",
		"internal/docs/templates/skills/sync.md.tmpl",
	}
	const overclaim = "sees the config keys"
	const accurate = "`mtix sync init`, `clone`, `pull` or `push` connects"
	for _, rel := range files {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
			require.NoError(t, err)
			text := strings.Join(strings.Fields(string(body)), " ")
			require.NotContains(t, text, overclaim, "a sync command does not store the mark on sight")
			if rel != "CHANGELOG.md" {
				require.Contains(t, text, accurate)
			}
		})
	}
}
