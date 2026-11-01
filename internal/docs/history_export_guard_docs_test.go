// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that the history.export rollout guard (MTIX-95.31.19.6) is taught by
// every shipped surface: manual, skills and mirrors, troubleshooting, export format.
package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistoryExportGuardDocs_EverySurfaceTeachesGuardRefusalAndBlindSpot(t *testing.T) {
	set := loadShippedDocs(t)
	for _, path := range []string{
		"USERMANUAL.md", "docs/EXPORT-FORMAT.md",
		"internal/docs/templates/troubleshooting.md.tmpl",
		"internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md",
	} {
		doc, ok := set.all[path]
		require.True(t, ok, path)
		require.Contains(t, doc, "history.export.guard", path)
		require.Contains(t, doc, "0.6.0", path, "names the minimum version")
		require.Contains(t, doc, "through git", path, "states the blind spot")
		require.NotContains(t, doc, "no doctor check verifies that yet", path)
		require.NotContains(t, doc, "writer recorded in", path, "the file's writer is not judged")
		require.NotContains(t, strings.ToLower(doc), "supabase", path)
	}
}

// The switch reaches other clones through the tracked config.yaml without their
// guard running; the troubleshooting and admin guidance says to run doctor there.
func TestHistoryExportGuardDocs_PropagationThroughConfigYamlIsDocumented(t *testing.T) {
	set := loadShippedDocs(t)
	for _, path := range []string{
		"internal/docs/templates/troubleshooting.md.tmpl", "docs/TROUBLESHOOTING.md",
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md",
	} {
		doc, ok := set.all[path]
		require.True(t, ok, path)
		require.Contains(t, doc, "tracked `.mtix/config.yaml`", path)
		require.Contains(t, doc, "mtix doctor", path)
		require.Contains(t, doc, "every clone", path)
	}
	require.Contains(t, set.all["docs/TROUBLESHOOTING.md"], "history.export refused", "the generated page carries the section")
}
