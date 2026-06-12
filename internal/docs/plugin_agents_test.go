// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for the Codex and pi plugin targets (MTIX-27, issue #15).
// Written RED-first per TDD-WORKFLOW.md §1.1.
//
// Codex consumes AGENTS.md natively and configures MCP servers via
// [mcp_servers.*] tables in .codex/config.toml (project-scoped) or
// ~/.codex/config.toml (global). pi loads AGENTS.md hierarchically and
// reaches MCP servers only through the community pi-mcp-adapter
// extension, so its install emits guidance instead of config.
package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultByFile indexes install results for assertion convenience.
func resultByFile(results []InstallResult) map[string]InstallResult {
	m := make(map[string]InstallResult, len(results))
	for _, r := range results {
		m[r.File] = r
	}
	return m
}

// TestPluginInstaller_Codex_WritesAgentsAndConfig: happy path — a fresh
// project gets AGENTS.md at the root and a project-scoped
// .codex/config.toml containing the mtix MCP server stanza.
func TestPluginInstaller_Codex_WritesAgentsAndConfig(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	results, err := installer.Install("codex", false)
	require.NoError(t, err)

	byFile := resultByFile(results)

	agents := filepath.Join(projectDir, "AGENTS.md")
	require.FileExists(t, agents)
	assert.Equal(t, "installed", byFile["AGENTS.md"].Action)
	content, err := os.ReadFile(agents)
	require.NoError(t, err)
	assert.Contains(t, string(content), "mtix", "AGENTS.md must brief the agent on mtix")

	confPath := filepath.Join(projectDir, ".codex", "config.toml")
	require.FileExists(t, confPath)
	assert.Equal(t, "installed", byFile["config.toml"].Action)
	conf, err := os.ReadFile(confPath)
	require.NoError(t, err)
	assert.Contains(t, string(conf), "[mcp_servers.mtix]")
	assert.Contains(t, string(conf), `command = "mtix"`)
	assert.Contains(t, string(conf), `"mcp"`)
}

// TestPluginInstaller_Codex_NeverClobbersExistingFiles: error path — a
// user's existing AGENTS.md and config.toml must remain byte-identical;
// the config result downgrades to manual guidance carrying the stanza.
func TestPluginInstaller_Codex_NeverClobbersExistingFiles(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	codexDir := filepath.Join(projectDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0o755))

	userAgents := "# my own agents file\n"
	userConf := "# my own codex config\n[mcp_servers.other]\ncommand = \"other\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte(userAgents), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(userConf), 0o644))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	results, err := installer.Install("codex", false)
	require.NoError(t, err)

	gotAgents, err := os.ReadFile(filepath.Join(projectDir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, userAgents, string(gotAgents), "existing AGENTS.md must not be touched")

	gotConf, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, userConf, string(gotConf), "existing config.toml must not be touched")

	byFile := resultByFile(results)
	assert.Equal(t, "skipped", byFile["AGENTS.md"].Action)
	assert.Equal(t, "manual", byFile["config.toml"].Action)
	assert.Contains(t, byFile["config.toml"].Note, "[mcp_servers.mtix]",
		"manual guidance must carry the exact stanza to add")
}

// TestPluginInstaller_Codex_Global: --global targets ~/.codex/.
func TestPluginInstaller_Codex_Global(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	_, err := installer.Install("codex", true)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(home, ".codex", "config.toml"))
	assert.FileExists(t, filepath.Join(home, ".codex", "AGENTS.md"),
		"global Codex instructions live at ~/.codex/AGENTS.md")
}

// TestPluginInstaller_Pi_InstallsAgentsAndAdapterGuidance: pi gets
// AGENTS.md (which it loads natively) and a manual result pointing at
// pi-mcp-adapter — pi has no built-in MCP by design.
func TestPluginInstaller_Pi_InstallsAgentsAndAdapterGuidance(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	results, err := installer.Install("pi", false)
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(projectDir, "AGENTS.md"))

	byFile := resultByFile(results)
	assert.Equal(t, "manual", byFile["mcp"].Action)
	assert.Contains(t, byFile["mcp"].Note, "pi-mcp-adapter")
	assert.Contains(t, byFile["mcp"].Note, "mtix mcp",
		"guidance must include the mtix MCP server command")
}

// TestPluginInstaller_Pi_Global: --global writes pi's global agent file
// location ~/.pi/agent/AGENTS.md.
func TestPluginInstaller_Pi_Global(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	_, err := installer.Install("pi", true)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(home, ".pi", "agent", "AGENTS.md"))
}

// TestPluginInstaller_Pi_ExistingAgentsPreserved: write-if-absent applies
// to pi as well.
func TestPluginInstaller_Pi_ExistingAgentsPreserved(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	user := "# mine\n"
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte(user), 0o644))

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	results, err := installer.Install("pi", false)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(projectDir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, user, string(got))
	assert.Equal(t, "skipped", resultByFile(results)["AGENTS.md"].Action)
}

// TestPluginInstaller_UnsupportedTarget_ListsRealTargets: the error must
// advertise exactly the implemented targets — no aspirational entries.
func TestPluginInstaller_UnsupportedTarget_ListsRealTargets(t *testing.T) {
	installer := NewPluginInstaller(t.TempDir(), minimalTemplateData(), nil)
	_, err := installer.Install("emacs", false)
	require.Error(t, err)
	for _, want := range []string{"claude-code", "codex", "pi"} {
		assert.Contains(t, err.Error(), want)
	}
}

// TestPluginInstaller_Codex_UnwritableDir_ReturnsError: error path —
// filesystem failures surface instead of half-installing.
func TestPluginInstaller_Codex_UnwritableDir_ReturnsError(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o555)) // read-only
	t.Cleanup(func() { _ = os.Chmod(projectDir, 0o755) })

	installer := NewPluginInstaller(projectDir, minimalTemplateData(), nil)
	_, err := installer.Install("codex", false)
	require.Error(t, err)
}

// TestInstallConfigIfAbsent_UnwritableParent_ReturnsError covers the
// config-write error path directly.
func TestInstallConfigIfAbsent_UnwritableParent_ReturnsError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "ro")
	require.NoError(t, os.MkdirAll(parent, 0o555))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	_, err := installConfigIfAbsent(filepath.Join(parent, "sub", "config.toml"), "x", "note")
	require.Error(t, err)
}

// TestInstallAgentsFile_NilTemplates_ReturnsError: construction-time
// template failures surface at install, not as a panic.
func TestInstallAgentsFile_NilTemplates_ReturnsError(t *testing.T) {
	p := &PluginInstaller{projectDir: t.TempDir(), data: minimalTemplateData()}
	_, err := p.installAgentsFile(p.projectDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "templates")
}
