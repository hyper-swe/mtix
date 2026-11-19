// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A program that resolves inside the project is reviewed, not trusted by name;
// every agent-facing copy and the changelog must say so.
func TestProgramReviewDocs_ProgramResolvingInsideProjectIsReviewed(t *testing.T) {
	for _, rel := range []string{
		"USERMANUAL.md",
		".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md",
		"internal/docs/templates/skills/admin.md.tmpl",
	} {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel))) //nolint:gosec // fixed repo paths
			require.NoError(t, err)
			require.Contains(t, string(body), "A program that resolves inside the project (for\nexample through a PATH directory that links into it) is never known by its\nname; its notice says to name that file as a pinned project script. A hook that cannot be classified")
			require.Contains(t, string(body), "is listed with the pin step (name it as a project script), not with an approval command.")
		})
	}
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	require.NoError(t, err)
	require.Contains(t, string(changelog), "MTIX-112.34.1.1")
	require.Contains(t, string(changelog), "the strict-mode status lists such a program with the pin step instead of a program approval")
	require.Contains(t, string(changelog), "its notice says to name the file as a pinned project script")
}
