// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
)

// TestUnblockHelp_ShippedGuidanceUsesRealDependencyCommand catches accidental
// obsolete guidance in shipped source, templates, skills and docs (FR-13.1).
// Board history and private loop evidence are outside shipped guidance.
func TestUnblockHelp_ShippedGuidanceUsesRealDependencyCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	obsolete := strings.Join([]string{"mtix", "deps"}, " ")
	for _, name := range []string{"cmd", "internal/docs/templates", "docs", ".claude-plugin", ".codex-plugin", "USERMANUAL.md", "README.md"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, name), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".md", ".tmpl":
			default:
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			require.NotContains(t, string(content), obsolete, path)
			return nil
		}))
	}
}

// commandReferencePaths recognizes the quoted invocations shipped by unblock.
// It checks ordinary accidental guidance drift, not arbitrary natural language.
func commandReferencePaths(text string) [][]string {
	quoted := regexp.MustCompile("['`]mtix ([^'`]+)['`]")
	paths := [][]string{}
	for _, match := range quoted.FindAllStringSubmatch(text, -1) {
		path := []string{}
		for _, token := range strings.Fields(match[1]) {
			if strings.HasPrefix(token, "<") || strings.HasPrefix(token, "-") {
				break
			}
			path = append(path, token)
		}
		paths = append(paths, path)
	}
	return paths
}

func resolvesCompleteCommandPath(path []string) bool {
	cmd, remaining, err := newRootCmd().Find(path)
	return err == nil && len(remaining) == 0 && cmd.CommandPath() == "mtix "+strings.Join(path, " ")
}

func TestUnblockHelp_AllReferencedCommandPathsExist(t *testing.T) {
	help, err := executeCmd(newRootCmd(), "unblock", "--help")
	require.NoError(t, err)
	paths := commandReferencePaths(help)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		require.True(t, resolvesCompleteCommandPath(path), "full command path %v must resolve", path)
	}
	for _, tt := range []struct {
		path  []string
		valid bool
	}{
		{[]string{"dep", "show"}, true},
		{[]string{"dep", "missing"}, false},
		{[]string{strings.Join([]string{"dep", "s"}, "")}, false},
	} {
		require.Equal(t, tt.valid, resolvesCompleteCommandPath(tt.path), "%v", tt.path)
	}
}

// generatedHelpReference renders the actual embedded Cobra documentation,
// isolated from tracked source and runtime project state (FR-13.2).
func generatedHelpReference(t *testing.T) string {
	t.Helper()
	data := docs.BuildTemplateData(newRootCmd(), mcp.NewToolRegistry(), "PROJ", "0.5.4-beta")
	output := t.TempDir()
	if evidence := os.Getenv("MTIX_HELP_GENERATED_DIR"); evidence != "" {
		output = evidence
	}
	gen, err := docs.NewEmbeddedGenerator(output, data, nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(output, "CLI_REFERENCE.md"))
	require.NoError(t, err)
	return string(content)
}

func cliReferenceSection(t *testing.T, reference, name, use string) string {
	t.Helper()
	marker := "## " + name + "\n\n**Usage:** `" + use + "`\n"
	start := strings.Index(reference, marker)
	require.NotEqual(t, -1, start, marker)
	section := reference[start:]
	end := strings.Index(section, "\n---\n")
	require.NotEqual(t, -1, end)
	return section[:end+len("\n---\n")]
}

func TestUnblockHelp_CLIReferenceMatchesActualGenerator(t *testing.T) {
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	cmd := newUnblockCmd()
	require.Equal(t, cliReferenceSection(t, generatedHelpReference(t), cmd.Name(), cmd.Use), cliReferenceSection(t, string(actual), cmd.Name(), cmd.Use))
}

func TestUnblockCmd_StillBlockedGuidanceResolvesRealCommand(t *testing.T) {
	initTestApp(t)
	for _, title := range []string{"blocked", "blocker"} {
		require.NoError(t, runCreate(title, "", "", 3, "", "", "", "", ""))
	}
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
	var err error
	output := captureStdout(t, func() { _, err = executeCmd(newUnblockCmd(), "TEST-2") })
	require.NoError(t, err)
	require.Contains(t, output, "still blocked")
	require.Contains(t, output, "mtix dep show TEST-2")
	paths := commandReferencePaths(output)
	require.Len(t, paths, 1)
	// The concrete ID is an argument, so resolve the two-word command path.
	require.Equal(t, []string{"dep", "show", "TEST-2"}, paths[0])
	require.True(t, resolvesCompleteCommandPath(paths[0][:2]))
}
