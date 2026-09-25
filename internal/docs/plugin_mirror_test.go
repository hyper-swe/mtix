// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPluginInstaller_AdminSkill_MatchesPluginMirror: the admin skill in
// .claude-plugin/skills is byte-identical to the admin skill template
// rendered for this repository's prefix (MTIX), so the plugin ships what
// the template says (MTIX-95.1.4).
func TestPluginInstaller_AdminSkill_MatchesPluginMirror(t *testing.T) {
	data := minimalTemplateData()
	data.ProjectPrefix = "MTIX"
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, data, nil).Install("claude-code", false)
	require.NoError(t, err)
	rendered, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "mtix-admin.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	mirror, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "skills", "mtix-admin.md"))
	require.NoError(t, err)
	require.Equal(t, string(rendered), string(mirror), ".claude-plugin/skills/mtix-admin.md matches the rendered template")
}
