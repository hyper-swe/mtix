// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// These tests keep shipped MCP instructions aligned with the registry wired
// by the running binary (FR-13.1, MTIX-107.90), including plugin frontmatter.
import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

func loadMCPInstructionDocs(root string) (map[string]string, error) {
	documents := make(map[string]string)
	for _, dir := range []string{"internal/docs/templates", ".claude-plugin", ".codex-plugin", "docs"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || (filepath.Ext(path) != ".md" && filepath.Ext(path) != ".tmpl") {
				return nil
			}
			return readMCPInstructionDoc(root, path, documents)
		})
		if err != nil {
			return nil, fmt.Errorf("walk shipped docs %s: %w", dir, err)
		}
	}
	for _, name := range []string{"README.md", "USERMANUAL.md"} {
		if err := readMCPInstructionDoc(root, filepath.Join(root, name), documents); err != nil {
			return nil, fmt.Errorf("read shipped doc %s: %w", name, err)
		}
	}
	return documents, nil
}

func readMCPInstructionDoc(root, path string, documents map[string]string) error {
	body, err := os.ReadFile(path) //nolint:gosec // test inventory reads only repository or fixture roots
	if err != nil {
		return fmt.Errorf("read instruction %s: %w", path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("relative instruction path %s: %w", path, err)
	}
	documents[filepath.ToSlash(rel)] = string(body)
	return nil
}

func unknownMCPInstructionTools(documents map[string]string, reg *mcp.ToolRegistry) []string {
	registered := make(map[string]bool)
	for _, tool := range reg.List() {
		registered["mcp__mtix__"+tool.Name] = true
	}
	toolName := regexp.MustCompile(`mcp__mtix__[A-Za-z0-9_-]+`)
	var problems []string
	for path, text := range documents {
		for i, line := range strings.Split(text, "\n") {
			for _, name := range toolName.FindAllString(line, -1) {
				if !registered[name] {
					problems = append(problems, fmt.Sprintf("%s:%d: %s", path, i+1, name))
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func TestMCPInstructionDocs_EveryToolExistsInBuild(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	documents, err := loadMCPInstructionDocs(root)
	require.NoError(t, err)
	for _, path := range []string{
		"internal/docs/templates/skill.md.tmpl", ".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md", "docs/SKILL.md", "docs/CLI_REFERENCE.md",
		"README.md", "USERMANUAL.md",
	} {
		require.Contains(t, documents, path, "the inventory must reach every shipped surface")
	}
	require.Contains(t, documents["internal/docs/templates/skill.md.tmpl"], "mcp__mtix__mtix_context")
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	require.NotEmpty(t, reg.List())
	require.Empty(t, unknownMCPInstructionTools(documents, reg), "shipped docs name tools absent from this build")
}

func TestMCPInstructionDocs_CheckerRejectsPlantedUnknownTool(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"known tools", "`mcp__mtix__mtix_context` and mcp__mtix__mtix_inbox", nil},
		{"empty text", "", nil},
		{"prefix description", "Use the mcp__mtix__ prefix", nil},
		{"prose", "Call mcp__mtix__mtix_planted_unknown.", []string{"fixture.md:1: mcp__mtix__mtix_planted_unknown"}},
		{"frontmatter", "allowed-tools:\n  - mcp__mtix__mtix_planted_unknown\n", []string{"fixture.md:2: mcp__mtix__mtix_planted_unknown"}},
		{"code block", "```\nmcp__mtix__mtix_planted_unknown\n```", []string{"fixture.md:2: mcp__mtix__mtix_planted_unknown"}},
		{"known prefix unknown suffix", "mcp__mtix__mtix_show_unknown", []string{"fixture.md:1: mcp__mtix__mtix_show_unknown"}},
		{"multiple tools", "mcp__mtix__mtix_show mcp__mtix__mtix_planted_unknown", []string{"fixture.md:1: mcp__mtix__mtix_planted_unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, unknownMCPInstructionTools(map[string]string{"fixture.md": tt.text}, reg))
		})
	}
}

func TestMCPInstructionDocs_InventoryIncludesNestedDocsAndReferences(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		"internal/docs/templates/nested/instruction.md.tmpl", ".claude-plugin/skills/nested/SKILL.md",
		".codex-plugin/skills/nested/SKILL.md", "docs/nested/instruction.md", "docs/CLI_REFERENCE.md",
		"README.md", "USERMANUAL.md",
	}
	for _, path := range paths {
		file := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o750))
		require.NoError(t, os.WriteFile(file, []byte("mcp__mtix__mtix_planted_unknown"), 0o600))
	}
	documents, err := loadMCPInstructionDocs(root)
	require.NoError(t, err)
	require.Len(t, documents, len(paths))
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	for _, path := range paths {
		require.Equal(t, "mcp__mtix__mtix_planted_unknown", documents[path])
		require.Contains(t, unknownMCPInstructionTools(documents, reg), path+":1: mcp__mtix__mtix_planted_unknown")
	}
}
