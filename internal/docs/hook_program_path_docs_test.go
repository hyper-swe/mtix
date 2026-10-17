// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Hook program paths must be clean; every agent-facing copy says so.
func TestHookProgramPathDocs_StateThePathMustBeClean(t *testing.T) {
	for _, rel := range []string{
		"USERMANUAL.md",
		".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/admin.md.tmpl",
	} {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
			require.NoError(t, err)
			require.Contains(t, string(body), "Hook program paths must be clean")
			require.Contains(t, string(body), "parent-directory\nsegment")
		})
	}
}
