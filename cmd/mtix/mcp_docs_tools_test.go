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

// isMCPDocDataReference exempts only the existing schema-key and SQL-role
// declarations, identified by their repository path and exact line. The same
// identifier in a tool instruction, or a prefixed name, remains an error.
func isMCPDocDataReference(path, line, name string) bool {
	switch path {
	case "docs/EXPORT-FORMAT.md":
		return name == "mtix_version" && line == "| `mtix_version` | Version of the mtix that wrote the file (may be empty). |"
	case "docs/SECURITY-MODEL.md":
		return name == "mtix_writer" && line == "As statements, run by the table owner (replace `public` if the hub lives in another schema, and `mtix_writer` with the syncing role). This is the same SQL the sync skill gives, and a test compares the two:"
	case "internal/docs/templates/workflows/safety-critical.md.tmpl":
		return name == "mtix_sync" && line == "Provision the hub, create the least-privilege role (`mtix_sync`),"
	case "internal/docs/templates/workflows/small-team.md.tmpl":
		return name == "mtix_sync" && (line == "Run as a PG superuser once during provisioning. `mtix_sync` runs" ||
			line == "the roles named here; `mtix_sync` owns only the objects `mtix sync init`")
	default:
		return false
	}
}

func unknownMCPInstructionTools(documents map[string]string, reg *mcp.ToolRegistry) []string {
	registered := make(map[string]bool)
	for _, tool := range reg.List() {
		registered[tool.Name] = true
	}
	prefixedName := regexp.MustCompile(`mcp__mtix__[A-Za-z0-9_-]+`)
	bareName := regexp.MustCompile("`(mtix_[A-Za-z0-9_-]+)`")
	var problems []string
	for path, text := range documents {
		for i, line := range strings.Split(text, "\n") {
			for _, name := range prefixedName.FindAllString(line, -1) {
				if !registered[strings.TrimPrefix(name, "mcp__mtix__")] {
					problems = append(problems, fmt.Sprintf("%s:%d: %s", path, i+1, name))
				}
			}
			for _, match := range bareName.FindAllStringSubmatch(line, -1) {
				name := match[1]
				if !registered[name] && !isMCPDocDataReference(path, line, name) {
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
		"README.md", "USERMANUAL.md", "docs/AGENTS.md", "docs/SYNC-DESIGN.md",
	} {
		require.Contains(t, documents, path, "the inventory must reach every shipped surface")
	}
	require.Contains(t, documents["internal/docs/templates/skill.md.tmpl"], "mcp__mtix__mtix_context")
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	require.Contains(t, documents["docs/AGENTS.md"], "`mtix_context`")
	require.Contains(t, documents["docs/SYNC-DESIGN.md"], "CONFLICT block")
	require.Greater(t, len(reg.List()), 30, "the scan uses the fully wired runtime registry")
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
		{"bare known tools", "`mtix_context` and `mtix_inbox`", nil},
		{"bare prose unknown", "Call `mtix_planted_unknown`.", []string{"fixture.md:1: mtix_planted_unknown"}},
		{"bare known prefix unknown suffix", "`mtix_show_unknown`", []string{"fixture.md:1: mtix_show_unknown"}},
		{"bare fenced unknown", "```\nCall `mtix_planted_unknown`\n```", []string{"fixture.md:2: mtix_planted_unknown"}},
		{"mixed representations", "`mtix_planted_unknown` mcp__mtix__mtix_show_unknown", []string{"fixture.md:1: mcp__mtix__mtix_show_unknown", "fixture.md:1: mtix_planted_unknown"}},
		{"CLI command", "`mtix verify` and `mtix dep show <id>`", nil},
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
		"README.md", "USERMANUAL.md", "docs/AGENTS.md", "docs/SYNC-DESIGN.md",
	}
	for _, path := range paths {
		file := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o750))
		require.NoError(t, os.WriteFile(file, []byte("mcp__mtix__mtix_planted_unknown\nCall `mtix_planted_unknown`."), 0o600))
	}
	documents, err := loadMCPInstructionDocs(root)
	require.NoError(t, err)
	require.Len(t, documents, len(paths))
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	for _, path := range paths {
		require.Equal(t, "mcp__mtix__mtix_planted_unknown\nCall `mtix_planted_unknown`.", documents[path])
		require.Contains(t, unknownMCPInstructionTools(documents, reg), path+":1: mcp__mtix__mtix_planted_unknown")
		require.Contains(t, unknownMCPInstructionTools(documents, reg), path+":2: mtix_planted_unknown")
	}
}

func TestMCPInstructionDocs_DataReferencesExemptOnlyExactDeclarations(t *testing.T) {
	initTestApp(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	tests := []struct {
		path        string
		name        string
		declaration string
	}{
		{"docs/EXPORT-FORMAT.md", "mtix_version", "| `mtix_version` | Version of the mtix that wrote the file (may be empty). |"},
		{"docs/SECURITY-MODEL.md", "mtix_writer", "As statements, run by the table owner (replace `public` if the hub lives in another schema, and `mtix_writer` with the syncing role). This is the same SQL the sync skill gives, and a test compares the two:"},
		{"internal/docs/templates/workflows/safety-critical.md.tmpl", "mtix_sync", "Provision the hub, create the least-privilege role (`mtix_sync`),"},
		{"internal/docs/templates/workflows/small-team.md.tmpl", "mtix_sync", "Run as a PG superuser once during provisioning. `mtix_sync` runs"},
		{"internal/docs/templates/workflows/small-team.md.tmpl", "mtix_sync", "the roles named here; `mtix_sync` owns only the objects `mtix sync init`"},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%s-%d", tt.name, i), func(t *testing.T) {
			require.True(t, isMCPDocDataReference(tt.path, tt.declaration, tt.name))
			require.False(t, isMCPDocDataReference(tt.path, tt.declaration, "mtix_planted_unknown"))
			require.Empty(t, unknownMCPInstructionTools(map[string]string{tt.path: tt.declaration}, reg))
			require.Equal(t, []string{tt.path + ":2: mtix_planted_unknown"},
				unknownMCPInstructionTools(map[string]string{tt.path: tt.declaration + "\nCall `mtix_planted_unknown`."}, reg))
			for _, instruction := range []string{
				"Call `" + tt.name + "`.", tt.declaration + " Call `" + tt.name + "`.",
				"Call mcp__mtix__" + tt.name + ".",
			} {
				require.NotEmpty(t, unknownMCPInstructionTools(map[string]string{tt.path: instruction}, reg), instruction)
			}
			require.NotEmpty(t, unknownMCPInstructionTools(map[string]string{"other.md": tt.declaration}, reg))
		})
	}
}
