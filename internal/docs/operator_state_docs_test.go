// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package docs

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOperatorStateDocs_Guidance(t *testing.T) {
	docs := loadShippedDocs(t)
	for _, path := range []string{"USERMANUAL.md", "internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md"} {
		require.Contains(t, docs.all[path], "Operator-local state", path)
		require.Contains(t, docs.all[path], "mtix hooks status", path)
		require.Contains(t, docs.all[path], "dotfile-sync", path)
	}
}
