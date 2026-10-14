// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Placement tests resolve generated links against actual files and protect native pointers.
package docs

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func assertProjectLink(t *testing.T, source, target string) {
	t.Helper()
	content, readErr := os.ReadFile(source)
	require.NoError(t, readErr)
	require.Contains(t, string(content), "]("+target+")")
	decoded, decodeErr := url.PathUnescape(target)
	require.NoError(t, decodeErr)
	info, statErr := os.Stat(filepath.Join(filepath.Dir(source), filepath.FromSlash(decoded)))
	require.NoError(t, statErr)
	require.True(t, info.Mode().IsRegular())
}

func TestGenerator_ProjectPaths_ResolveActualOutput(t *testing.T) {
	cases := []struct {
		name, dir, rootLink, backLink string
		relative, relativeRoot        bool
	}{
		{"canonical", ".mtix/docs", ".mtix/docs/WORKFLOWS.md", "../../MTIX.md", false, false},
		{"custom", "custom/references", "custom/references/WORKFLOWS.md", "../../MTIX.md", false, false},
		{"nested", "custom/deep/references", "custom/deep/references/WORKFLOWS.md", "../../../MTIX.md", false, false},
		{"relative", "references", "references/WORKFLOWS.md", "../MTIX.md", true, false},
		{"relative root", "references", "references/WORKFLOWS.md", "../MTIX.md", true, true},
		{"spaces", "custom refs/references", "custom%20refs/references/WORKFLOWS.md", "../../MTIX.md", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			refs := filepath.Join(root, filepath.FromSlash(tc.dir))
			output := refs
			if tc.relative {
				t.Chdir(root)
				output = tc.dir
			}
			data := minimalTemplateData()
			gen, createErr := NewEmbeddedGenerator(output, data, nil)
			require.NoError(t, createErr)
			projectRoot := root
			if tc.relativeRoot {
				projectRoot = "."
			}
			gen.SetProjectRoot(projectRoot)
			_, generateErr := gen.Generate(false)
			require.NoError(t, generateErr)
			assertProjectLink(t, filepath.Join(root, "MTIX.md"), tc.rootLink)
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				assertProjectLink(t, filepath.Join(refs, name), tc.backLink)
			}
			require.Empty(t, data.DocsPath, "render placement must not mutate caller data")
			pointers, readErr := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
			require.NoError(t, readErr)
			require.Equal(t, "@MTIX.md\n", string(pointers))
		})
	}
}

func TestGenerator_ProjectPaths_ReferenceOnlyDoesNotLinkMissingBriefing(t *testing.T) {
	for _, kind := range []string{"embedded", "filesystem"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			refs := filepath.Join(root, "custom", "references")
			var gen *Generator
			var createErr error
			if kind == "embedded" {
				gen, createErr = NewEmbeddedGenerator(refs, minimalTemplateData(), nil)
			} else {
				gen, createErr = NewGenerator("templates", refs, minimalTemplateData(), nil)
			}
			require.NoError(t, createErr)
			_, generateErr := gen.Generate(false)
			require.NoError(t, generateErr)
			require.NoFileExists(t, filepath.Join(root, "MTIX.md"))
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				content, readErr := os.ReadFile(filepath.Join(refs, name))
				require.NoError(t, readErr)
				require.NotContains(t, string(content), "](../../MTIX.md)")
				require.NotContains(t, string(content), "[MTIX.md](")
			}
			assertProjectLink(t, filepath.Join(refs, "AGENTS.md"), "WORKFLOWS.md")
			assertProjectLink(t, filepath.Join(refs, "CLAUDE.md"), "AGENTS.md")
		})
	}
}

func TestGenerator_ProjectPaths_SameRootRefusedBeforeWrites(t *testing.T) {
	cases := []struct {
		name         string
		alias, force bool
	}{
		{"same root", false, false}, {"same root forced", false, true}, {"alias", true, false}, {"alias forced", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			output := root
			if tc.alias {
				output = filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(root, output))
			}
			for _, name := range []string{"MTIX.md", "CLAUDE.md", "AGENTS.md"} {
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("original "+name), 0600))
			}
			gen, createErr := NewEmbeddedGenerator(output, minimalTemplateData(), nil)
			require.NoError(t, createErr)
			gen.SetProjectRoot(root)
			_, generateErr := gen.Generate(tc.force)
			require.ErrorContains(t, generateErr, "project root")
			for _, name := range []string{"MTIX.md", "CLAUDE.md", "AGENTS.md"} {
				content, readErr := os.ReadFile(filepath.Join(root, name))
				require.NoError(t, readErr)
				require.Equal(t, "original "+name, string(content))
			}
			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			require.Len(t, entries, 3)
		})
	}
}

func TestPluginInstall_ProjectPaths_KeepCanonicalReferences(t *testing.T) {
	for _, target := range []string{"claude-code", "codex", "pi"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			refs := filepath.Join(root, ".mtix", "docs")
			gen, createErr := NewEmbeddedGenerator(refs, minimalTemplateData(), nil)
			require.NoError(t, createErr)
			_, generateErr := gen.Generate(false)
			require.NoError(t, generateErr)
			_, installErr := NewPluginInstaller(root, minimalTemplateData(), nil).Install(target, false)
			require.NoError(t, installErr)
			assertProjectLink(t, filepath.Join(root, "MTIX.md"), ".mtix/docs/WORKFLOWS.md")
			content, readErr := os.ReadFile(filepath.Join(root, "AGENTS.md"))
			require.NoError(t, readErr)
			require.True(t, strings.HasPrefix(string(content), "Read [MTIX.md](MTIX.md)"))
		})
	}
}

func TestGenerator_ProjectPaths_InvalidPlacementPreservesRoot(t *testing.T) {
	cases := []struct {
		name                                                        string
		nilData, rootFile, missingRoot, outputFile, badOutputParent bool
	}{
		{name: "nil data", nilData: true},
		{name: "root file", rootFile: true},
		{name: "missing root", missingRoot: true},
		{name: "output file", outputFile: true},
		{name: "output parent file", badOutputParent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "project")
			output := filepath.Join(base, "references")
			if tc.rootFile {
				require.NoError(t, os.WriteFile(root, []byte("user root"), 0600))
			} else if !tc.missingRoot {
				require.NoError(t, os.Mkdir(root, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("user instructions"), 0600))
			}
			if tc.outputFile {
				require.NoError(t, os.WriteFile(output, []byte("user reference"), 0600))
			}
			if tc.badOutputParent {
				require.NoError(t, os.WriteFile(output, []byte("parent"), 0600))
				output = filepath.Join(output, "child")
			}
			data := minimalTemplateData()
			if tc.nilData {
				data = nil
			}
			gen, createErr := NewEmbeddedGenerator(output, data, nil)
			require.NoError(t, createErr)
			gen.SetProjectRoot(root)
			_, generateErr := gen.Generate(false)
			require.Error(t, generateErr)
			if !tc.rootFile && !tc.missingRoot {
				content, readErr := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
				require.NoError(t, readErr)
				require.Equal(t, "user instructions", string(content))
				require.NoFileExists(t, filepath.Join(root, "MTIX.md"))
			}
		})
	}
}
