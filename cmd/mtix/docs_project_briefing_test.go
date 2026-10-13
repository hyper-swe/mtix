// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Project briefing integration tests verify pointer migration without changing user bytes.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func projectBriefingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func readProjectDoc(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(data)
}

func TestRunDocsGenerate_ProjectBriefing_MigratesAndPreservesBytes(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"fresh", "", "@MTIX.md\n"},
		{"custom", "# User\r\nlast", "# User\r\nlast\n@MTIX.md\n"},
		{"migration", "before\r\n<!-- AUTO-GENERATED: SYNC -->\r\nold\r\n<!-- END AUTO-GENERATED -->after", "before\r\nafter\n@MTIX.md\n"},
		{"detach", "# User\n@MTIX.md\n", "# User\n@MTIX.md\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := projectBriefingDir(t)
			if tc.input != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(tc.input), 0600))
			}
			require.NoError(t, runDocsGenerate(false))
			require.Equal(t, tc.want, readProjectDoc(t, dir, "CLAUDE.md"))
			require.Contains(t, readProjectDoc(t, dir, "AGENTS.md"), "MTIX.md")
			briefing := readProjectDoc(t, dir, "MTIX.md")
			require.Contains(t, briefing, "Context Chain")
			require.Contains(t, briefing, "Instructions for Claude")
			require.Contains(t, briefing, "](.mtix/docs/WORKFLOWS.md)")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "MTIX.md"), []byte("edited"), 0600))
			require.NoError(t, runDocsGenerate(false))
			require.Equal(t, briefing, readProjectDoc(t, dir, "MTIX.md"))
			require.Equal(t, tc.want, readProjectDoc(t, dir, "CLAUDE.md"))
		})
	}
}

func TestRunDocsGenerate_MalformedMarkers_RefusesBeforeRootWrites(t *testing.T) {
	cases := []string{"<!-- AUTO-GENERATED: SYNC -->open", "<!-- END AUTO-GENERATED -->", "<!-- AUTO-GENERATED: A --><!-- AUTO-GENERATED: B --><!-- END AUTO-GENERATED --><!-- END AUTO-GENERATED -->", "<!-- AUTO-GENERATED: SYNC", "<!-- AUTO-GENERATED: ! -->", "<!-- END AUTO-GENERATED", "<!-- END AUTO-GENERATED: WRONG -->"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			dir := projectBriefingDir(t)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("user"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(input), 0600))
			err := runDocsGenerate(false)
			require.ErrorContains(t, err, "marker")
			require.Equal(t, "user", readProjectDoc(t, dir, "CLAUDE.md"))
			require.Equal(t, input, readProjectDoc(t, dir, "AGENTS.md"))
			require.NoFileExists(t, filepath.Join(dir, "MTIX.md"))
		})
	}
}

func TestRunDocsGenerate_HyperSWE_SuppressesNewPointersPreservesExisting(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "existing"}[existing], func(t *testing.T) {
			dir := projectBriefingDir(t)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "HYPERSWE.md"), []byte("hyperswe"), 0600))
			if existing {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("@MTIX.md\n<!-- AUTO-GENERATED: SYNC -->old<!-- END AUTO-GENERATED -->user"), 0600))
			}
			require.NoError(t, runDocsGenerate(false))
			require.FileExists(t, filepath.Join(dir, "MTIX.md"))
			require.NoFileExists(t, filepath.Join(dir, "AGENTS.md"))
			if existing {
				require.Equal(t, "@MTIX.md\nuser", readProjectDoc(t, dir, "CLAUDE.md"))
			} else {
				require.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"))
			}
			require.Equal(t, "hyperswe", readProjectDoc(t, dir, "HYPERSWE.md"))
		})
	}
}

func TestGenerateInitDocs_ProjectBriefing_RespectsHyperSWE(t *testing.T) {
	for _, hs := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "hyperswe"}[hs], func(t *testing.T) {
			dir := projectBriefingDir(t)
			if hs {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "HYPERSWE.md"), []byte("hs"), 0600))
			}
			results := generateInitDocs(filepath.Join(dir, ".mtix", "docs"), "TEST", "test")
			require.NotEmpty(t, results)
			require.FileExists(t, filepath.Join(dir, "MTIX.md"))
			if hs {
				require.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"))
			} else {
				require.Equal(t, "@MTIX.md\n", readProjectDoc(t, dir, "CLAUDE.md"))
			}
		})
	}
}

func TestRunDocsGenerate_RootSymlink_Refuses(t *testing.T) {
	for _, name := range []string{"MTIX.md", "CLAUDE.md", "AGENTS.md"} {
		t.Run(name, func(t *testing.T) {
			dir := projectBriefingDir(t)
			target := filepath.Join(t.TempDir(), "victim")
			require.NoError(t, os.WriteFile(target, []byte("safe"), 0600))
			require.NoError(t, os.Symlink(target, filepath.Join(dir, name)))
			err := runDocsGenerate(false)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), "regular") || strings.Contains(err.Error(), "symlink"))
			data, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "safe", string(data))
		})
	}
}

func TestGenerateInitDocs_ArbitraryOutput_DoesNotWriteParentBriefings(t *testing.T) {
	grandparent := t.TempDir()
	parent := filepath.Join(grandparent, "project")
	require.NoError(t, os.Mkdir(parent, 0700))
	output := filepath.Join(parent, "references")
	results := generateInitDocs(output, "TEST", "test")
	require.NotEmpty(t, results)
	for _, dir := range []string{parent, grandparent} {
		for _, name := range []string{"MTIX.md", "CLAUDE.md", "AGENTS.md"} {
			require.NoFileExists(t, filepath.Join(dir, name))
		}
	}
}

func TestRunDocsGenerate_ConfiguredProjectPrefix_PreservesInitHeaderAndExamples(t *testing.T) {
	saveAndResetApp(t)
	identityOperatorState(t)
	dir := projectBriefingDir(t)
	require.NoError(t, runInit("TEST"))
	initialized := readProjectDoc(t, dir, "MTIX.md")
	require.Contains(t, initialized, "> **Project:** TEST")
	configured, configErr := service.NewConfigService(filepath.Join(dir, ".mtix", "config.yaml"))
	require.NoError(t, configErr)
	app.configSvc = configured
	require.NoError(t, runDocsGenerate(false))
	regenerated := readProjectDoc(t, dir, "MTIX.md")
	require.Contains(t, regenerated, "> **Project:** TEST")
	require.Contains(t, regenerated, "`TEST-1.4`")
	require.Contains(t, regenerated, "`TEST-1.<token>`")
	require.Contains(t, regenerated, "mtix list --under TEST-1,TEST-2")
	require.Equal(t, initialized, regenerated)
}

func TestRunDocsGenerate_NoConfiguredPrefix_UsesFallback(t *testing.T) {
	saveAndResetApp(t)
	dir := projectBriefingDir(t)
	require.NoError(t, runDocsGenerate(false))
	require.Contains(t, readProjectDoc(t, dir, "MTIX.md"), "> **Project:** PROJ")
}

func TestGenerateInitDocs_ArbitraryOutput_KeepsSelfContainedReferences(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "custom", "references")
	results := generateInitDocs(output, "TEST", "test")
	require.NotEmpty(t, results)
	require.NoFileExists(t, filepath.Join(root, "MTIX.md"))
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		content, readErr := os.ReadFile(filepath.Join(output, name))
		require.NoError(t, readErr)
		require.NotContains(t, string(content), "[MTIX.md](")
	}
	claude, readErr := os.ReadFile(filepath.Join(output, "CLAUDE.md"))
	require.NoError(t, readErr)
	require.Contains(t, string(claude), "](AGENTS.md)")
	require.FileExists(t, filepath.Join(output, "AGENTS.md"))
}
