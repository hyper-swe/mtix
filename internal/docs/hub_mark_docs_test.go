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

// The hub mark is stored by sync doctor when it finds evidence (config keys, a
// secrets DSN or an applied-event record) and by sync init, clone, pull and push
// after a successful hub connection. Relay attach, sync status and a failed
// connect do not store it, so the docs must not claim that any sync command
// does, nor that doctor stores it on every run.
func TestHubMarkDocs_StoredBy_NamesTheCommandsThatStoreIt(t *testing.T) {
	files := []string{
		"USERMANUAL.md",
		"CHANGELOG.md",
		".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md",
		"internal/docs/templates/skills/sync.md.tmpl",
	}
	forbidden := []string{
		"sees the config keys",
		"sync commands store the mark the first time they see",
		"`mtix sync doctor` runs",
		"`mtix sync doctor` stores the mark the first time it runs",
	}
	required := []string{
		"finds evidence (sync or relay keys, a secrets DSN or an applied-event record)",
		"`mtix sync init`, `clone`, `pull` and `push` ",
		"`mtix sync relay attach`, `mtix sync status` and a failed connection do not",
	}
	for _, rel := range files {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
			require.NoError(t, err)
			text := strings.Join(strings.Fields(string(body)), " ")
			for _, f := range forbidden {
				require.NotContains(t, text, f, "overclaim about which commands store the mark")
			}
			for _, r := range required {
				alt := strings.NewReplacer(" and `push` ", " or `push` ", " and a failed connection do not", " and a connection that fails do not").Replace(r)
				require.Truef(t, strings.Contains(text, r) || strings.Contains(text, alt), "missing %q", r)
			}
		})
	}
}
