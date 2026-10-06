// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
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
	references, err := obsoleteHelpReferences(filepath.Join("..", ".."))
	require.NoError(t, err)
	require.Empty(t, references, "obsolete command guidance in shipped source")
}

func helpGuidanceRoots() []string {
	return []string{"cmd", "internal", "docs", ".claude-plugin", ".codex-plugin", "USERMANUAL.md", "README.md"}
}

func obsoleteHelpReferences(root string) ([]string, error) {
	references := []string{}
	obsolete := strings.Join([]string{"mtix", "deps"}, " ")
	for _, name := range helpGuidanceRoots() {
		err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry fs.DirEntry, err error) error {
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
				return fmt.Errorf("read shipped guidance %s: %w", path, err)
			}
			if strings.Contains(string(content), obsolete) {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return fmt.Errorf("locate shipped guidance %s: %w", path, err)
				}
				references = append(references, filepath.ToSlash(relative))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan shipped guidance %s: %w", name, err)
		}
	}
	return references, nil
}

// The scanner covers shipped internal Go as well as templates, while board
// history and private evidence remain outside the shipped-content boundary.
func TestUnblockHelp_ScanIncludesInternalSourceExcludesMetadata(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", stale), func(t *testing.T) {
			root := t.TempDir()
			for _, name := range helpGuidanceRoots() {
				if filepath.Ext(name) == ".md" {
					writeHelpScanFixture(t, root, name, "reference")
				} else {
					require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o755))
				}
			}
			obsolete := strings.Join([]string{"mtix", "deps"}, " ")
			guidance := "Show blocking dependencies for a node"
			if stale {
				guidance += " (see " + obsolete + ")"
			}
			writeHelpScanFixture(t, root, "internal/mcp/tools_dep.go", guidance)
			for _, name := range []string{".mtix/tasks.json", ".codex-lane/private.md", ".hyperswe/private.go"} {
				writeHelpScanFixture(t, root, name, obsolete)
			}
			references, err := obsoleteHelpReferences(root)
			require.NoError(t, err)
			expected := []string{}
			if stale {
				expected = append(expected, "internal/mcp/tools_dep.go")
			}
			require.Equal(t, expected, references)
		})
	}
}

func writeHelpScanFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// commandReferencePaths recognizes the quoted invocations shipped by unblock.
// Matching single, double and backtick quotes cover ordinary accidental guidance
// drift; this is not an arbitrary natural-language or shell parser.
func commandReferencePaths(text string) [][]string {
	quoted := regexp.MustCompile("'mtix ([^']+)'|\"mtix ([^\"]+)\"|`mtix ([^`]+)`")
	paths := [][]string{}
	for _, match := range quoted.FindAllStringSubmatch(text, -1) {
		reference := ""
		for _, capture := range match[1:] {
			if capture != "" {
				reference = capture
				break
			}
		}
		path := []string{}
		for _, token := range strings.Fields(reference) {
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

// Ordinary matching quote forms must retain every referenced command, including
// an extra invalid path alongside the original good single-quoted reference.
func TestUnblockHelp_AllOrdinaryQuotedPathsAreChecked(t *testing.T) {
	for _, quote := range []string{"'", "\"", "`"} {
		t.Run(quote, func(t *testing.T) {
			paths := commandReferencePaths("See " + quote + "mtix dep show <id>" + quote)
			require.Equal(t, [][]string{{"dep", "show"}}, paths)
			require.True(t, resolvesCompleteCommandPath(paths[0]))
			extra := "Original 'mtix dep show <id>'. Also " + quote + "mtix dep missing <id>" + quote
			paths = commandReferencePaths(extra)
			require.Equal(t, [][]string{{"dep", "show"}, {"dep", "missing"}}, paths)
			require.True(t, resolvesCompleteCommandPath(paths[0]))
			require.False(t, resolvesCompleteCommandPath(paths[1]))
		})
	}
	for _, malformed := range []string{"'mtix dep show <id>\"", "`mtix dep show <id>'", "\"mtix dep show <id>`"} {
		require.Empty(t, commandReferencePaths(malformed))
	}
}
