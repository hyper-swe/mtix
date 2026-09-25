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

// TestPluginInstaller_AdminSkill_StatesHubPrivilegeCoverage: the rendered
// admin skill states when strict mode fails, what superuser_membership
// covers, that the REPLICATION role attribute is outside the check, and
// the missing schema USAGE cause of a backup that finds no hub table
// (MTIX-95.1.4).
func TestPluginInstaller_AdminSkill_StatesHubPrivilegeCoverage(t *testing.T) {
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, minimalTemplateData(), nil).Install("claude-code", false)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "mtix-admin.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	skill := string(body)
	for _, phrase := range []string{
		"Setting it turns on strict mode for the doctor's `hub-privileges` check, which then fails whenever " +
			"`mtix sync harden` would report a finding, or when the check cannot run",
		"(its `hub-privileges` check then fails, instead of warning, whenever `mtix sync harden` would report a " +
			"finding, or when the check cannot run)",
		"`superuser_membership` (a role that can SET ROLE to a superuser, or that holds ADMIN OPTION on a role that can)",
		"The REPLICATION role attribute is outside the check too: a role that has it is checked for its privileges " +
			"and memberships like any other role, but not for the attribute.",
		"If the DSN's role lacks USAGE on the hub's schema, which leaves that schema off its search_path, the table " +
			"owner runs the GRANT statements the backup prints: USAGE on the schema, and SELECT on each sync table " +
			"and sync-table sequence by name",
	} {
		require.Contains(t, skill, phrase)
	}
	require.NotContains(t, skill, "when any other role can use the sync tables", "the narrower strict-mode description is gone")
	require.NotContains(t, skill, "ALL TABLES IN SCHEMA", "the grant names each sync table")
}
