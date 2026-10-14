// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Root briefing tests cover migration plans shared by CLI and local plugin installs.
package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginInstall_ProjectBriefing_AllLocalTargets(t *testing.T) {
	for _, target := range []string{"claude-code", "codex", "pi"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("user\r\n"), 0600))
			results, err := NewPluginInstaller(dir, minimalTemplateData(), nil).Install(target, false)
			require.NoError(t, err)
			require.NotEmpty(t, results)
			data, err := os.ReadFile(filepath.Join(dir, "MTIX.md"))
			require.NoError(t, err)
			require.Contains(t, string(data), "Context Chain")
			pointers, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			require.NoError(t, err)
			require.Equal(t, "user\r\n"+agentsPointer+"\n", string(pointers))
			claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
			require.NoError(t, err)
			require.Equal(t, "@MTIX.md\n", string(claude))
		})
	}
}

func TestStripProjectMarkers_UnrelatedBlocks_PreservesBytes(t *testing.T) {
	owned := map[string]string{"SYNC": ""}
	cases := []struct{ name, input, want string }{
		{"unrelated", "user<!-- AUTO-GENERATED: OTHER -->data<!-- END AUTO-GENERATED -->tail", "user<!-- AUTO-GENERATED: OTHER -->data<!-- END AUTO-GENERATED -->tail"},
		{"mixed", "x<!-- AUTO-GENERATED: SYNC -->mtix<!-- END AUTO-GENERATED -->y<!-- AUTO-GENERATED: OTHER -->other<!-- END AUTO-GENERATED -->z", "xy<!-- AUTO-GENERATED: OTHER -->other<!-- END AUTO-GENERATED -->z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stripProjectMarkers([]byte(tc.input), owned)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}

func TestProjectBriefing_ShippedGuidance_ExplainsOwnership(t *testing.T) {
	for _, path := range []string{"templates/agents.md.tmpl", "templates/claude.md.tmpl", "templates/skills/sync.md.tmpl", "../../.claude-plugin/skills/mtix-sync.md", "../../.codex-plugin/skills/sync/SKILL.md", "../../USERMANUAL.md", "../../docs/MCP-SETUP.md"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(data), "MTIX.md")
			require.Contains(t, string(data), "HYPERSWE.md")
		})
	}
}

func TestAppendProjectPointer_IncidentalMentions_AppendsRealPointer(t *testing.T) {
	for _, input := range []string{"# MTIX.md is obsolete\n", "MTIX.md is a filename\n", "```md\n" + agentsPointer + "\n```\n"} {
		t.Run(input, func(t *testing.T) {
			got, err := appendProjectPointer([]byte(input), agentsPointer, "AGENTS.md")
			require.NoError(t, err)
			require.Equal(t, input+agentsPointer+"\n", string(got))
		})
	}
}

func TestGenerator_ProjectRoot_RefreshesBriefingPreservesReferencePolicy(t *testing.T) {
	root := t.TempDir()
	refs := filepath.Join(root, ".mtix", "docs")
	gen, err := NewEmbeddedGenerator(refs, minimalTemplateData(), nil)
	require.NoError(t, err)
	gen.SetProjectRoot(root)
	_, err = gen.Generate(false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(refs, "WORKFLOWS.md"), []byte("custom reference"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "MTIX.md"), []byte("changed briefing"), 0600))
	_, err = gen.Generate(false)
	require.NoError(t, err)
	reference, err := os.ReadFile(filepath.Join(refs, "WORKFLOWS.md"))
	require.NoError(t, err)
	require.Equal(t, "custom reference", string(reference))
	current, err := os.ReadFile(filepath.Join(root, "MTIX.md"))
	require.NoError(t, err)
	require.Contains(t, string(current), "Context Chain")
	agents, err := os.ReadFile(filepath.Join(refs, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(agents), "](../../MTIX.md)")
	_, err = gen.Generate(true)
	require.NoError(t, err)
	reference, err = os.ReadFile(filepath.Join(refs, "WORKFLOWS.md"))
	require.NoError(t, err)
	require.NotEqual(t, "custom reference", string(reference))
}

func TestPluginInstall_ProjectBriefing_HyperSWEMigratesWithoutNewPointers(t *testing.T) {
	for _, target := range []string{"claude-code", "codex", "pi"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "HYPERSWE.md"), []byte("owned by hyperswe"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("user<!-- AUTO-GENERATED: SYNC -->old<!-- END AUTO-GENERATED -->tail"), 0600))
			installer := NewPluginInstaller(dir, minimalTemplateData(), nil)
			_, err := installer.Install(target, false)
			require.NoError(t, err)
			data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			require.NoError(t, err)
			require.Equal(t, "usertail", string(data))
			require.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"))
			_, err = installer.Install(target, false)
			require.NoError(t, err)
			data, err = os.ReadFile(filepath.Join(dir, "HYPERSWE.md"))
			require.NoError(t, err)
			require.Equal(t, "owned by hyperswe", string(data))
		})
	}
}

func TestAppendProjectPointer_DetachSentences_PreservesVerbatim(t *testing.T) {
	for _, line := range []string{agentsPointer, "See MTIX.md for mtix instructions.", "Read `MTIX.md` for mtix instructions."} {
		t.Run(line, func(t *testing.T) {
			original := "custom\r\n" + line
			got, err := appendProjectPointer([]byte(original), agentsPointer, "AGENTS.md")
			require.NoError(t, err)
			require.Equal(t, original, string(got))
		})
	}
}

func TestPluginInstall_ProjectExclusion_ReportsUserRuleNotice(t *testing.T) {
	dir := t.TempDir()
	_, initErr := projectGit(dir, "init", "-q")
	require.NoError(t, initErr)
	installer := NewPluginInstaller(dir, minimalTemplateData(), nil)
	_, installErr := installer.Install("pi", false)
	require.NoError(t, installErr)
	userRule := "/MTIX.md"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(userRule), 0600))
	_, addErr := projectGit(dir, "add", "AGENTS.md")
	require.NoError(t, addErr)
	results, resultErr := installer.Install("pi", false)
	require.NoError(t, resultErr)
	require.Contains(t, resultByFile(results)["MTIX.md"].Note, "MTIX.md remains ignored by user Git rules")
	content, readErr := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	require.NoError(t, readErr)
	require.Equal(t, userRule, string(content))
}
